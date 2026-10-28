// Package escalation lets an agent hand an incident on without a person relaying it, and pull a
// person in when it cannot tell what went wrong.
//
// escalate_incident decides nothing from the model's word. It re-checks the evidence itself, with
// Prometheus and Kubernetes queries, and posts to #incidents only when every check passes. This is
// the demo's policy: the alert text an agent reads is untrusted, so a crafted annotation must not be
// able to talk an agent into escalating. ask_oncall replies in the alert thread and mentions the
// team's on-call. After a posted escalation, a watcher posts "Resolved" to #incidents once the
// impacted service's error ratio has stayed under 1% for two minutes.
//
// Every message is built by the tool. Model text (summary) is cleaned and escaped as page_team
// does; mentions come only from the slack-ids file.
package escalation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/agentkourier/kourier-incident-demo/mcp/internal/catalog"
	"github.com/agentkourier/kourier-incident-demo/mcp/internal/pager"
	"github.com/agentkourier/kourier-incident-demo/mcp/internal/promapi"
	"github.com/agentkourier/kourier-incident-demo/mcp/internal/slackids"
)

// Thresholds of the evidence bar and the recovery watcher.
const (
	// ImpactedFailingRatio: the impacted service's 5xx ratio must be above this to escalate.
	ImpactedFailingRatio = 0.05
	// DependencyFailingRatio: the share of the impacted service's calls to the suspect that fail.
	DependencyFailingRatio = 0.5
	// SuspectHealthyRatio: the suspect's own 5xx ratio must be below this.
	SuspectHealthyRatio = 0.05
	// RolloutBefore and RolloutAfter bound the suspect's rollout around the errors' onset.
	RolloutBefore = 15 * time.Minute
	RolloutAfter  = time.Minute
	// ResolvedRatio and ResolvedFor: the impacted ratio must stay under ResolvedRatio for ResolvedFor.
	ResolvedRatio = 0.01
	ResolvedFor   = 2 * time.Minute
)

// Defaults for Config durations.
const (
	DefaultPollEvery        = 15 * time.Second
	DefaultMaxWatch         = 60 * time.Minute
	DefaultEscalationWindow = 30 * time.Minute
	DefaultAskWindow        = 10 * time.Minute
	onsetLookback           = 60 * time.Minute
	onsetStep               = 15 * time.Second
	onsetGap                = time.Minute
)

// probeHandlers are left out of request ratios.
const probeHandlers = `/healthz|/readyz|/livez|/metrics`

// DefaultDependencyQueries maps "impacted->suspect" to a PromQL ratio of the impacted service's
// failing calls to the suspect. %s is the namespace. Only checkout->payments has a client metric
// in this demo.
var DefaultDependencyQueries = map[string]string{
	"checkout->payments": `(sum(rate(payments_client_requests_total{namespace="%[1]s",app="checkout",outcome!="ok"}[1m])) or vector(0))` +
		` / sum(rate(payments_client_requests_total{namespace="%[1]s",app="checkout"}[1m]))`,
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Prom reads Prometheus.
type Prom interface {
	Query(ctx context.Context, query string, t time.Time) (float64, bool, error)
	QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]promapi.Sample, error)
}

// Slack posts messages.
type Slack interface {
	PostMessage(ctx context.Context, channel, text string) (string, error)
	PostReply(ctx context.Context, channel, threadTS, text string) (string, error)
	Permalink(ctx context.Context, channel, ts string) (string, error)
}

// Services finds services in the catalog.
type Services interface {
	Service(name string) (catalog.Service, error)
}

// Config configures an Escalator. Zero durations take their defaults.
type Config struct {
	Namespace         string            // the only namespace queried, e.g. commerce
	IncidentsChannel  string            // channel name escalations and resolutions go to, e.g. incidents
	AskChannels       []string          // channel names ask_oncall may reply in, e.g. checkout-alerts
	DependencyQueries map[string]string // nil means DefaultDependencyQueries
	PollEvery         time.Duration
	MaxWatch          time.Duration
	EscalationWindow  time.Duration
	AskWindow         time.Duration
	Now               func() time.Time
	Logger            *slog.Logger
}

// Escalator implements the tools. It is safe for concurrent use.
type Escalator struct {
	cfg   Config
	svc   Services
	ids   *slackids.Config
	slack Slack
	prom  Prom
	kube  kubernetes.Interface
	base  context.Context // outlives tool calls; bounds the recovery watchers

	incidentsID string
	askIDs      map[string]string // channel ID to name

	mu       sync.Mutex
	lastEsc  map[string]time.Time
	lastAsk  map[string]time.Time
	watching map[string]bool
	wg       sync.WaitGroup
}

// New returns an Escalator. base bounds background watchers: cancel it to stop them.
func New(base context.Context, cfg Config, svc Services, ids *slackids.Config, slack Slack, prom Prom, kube kubernetes.Interface) (*Escalator, error) {
	if !nameRE.MatchString(cfg.Namespace) {
		return nil, fmt.Errorf("namespace %q is not a Kubernetes name", cfg.Namespace)
	}
	if cfg.DependencyQueries == nil {
		cfg.DependencyQueries = DefaultDependencyQueries
	}
	if cfg.PollEvery <= 0 {
		cfg.PollEvery = DefaultPollEvery
	}
	if cfg.MaxWatch <= 0 {
		cfg.MaxWatch = DefaultMaxWatch
	}
	if cfg.EscalationWindow <= 0 {
		cfg.EscalationWindow = DefaultEscalationWindow
	}
	if cfg.AskWindow <= 0 {
		cfg.AskWindow = DefaultAskWindow
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	incidents, ok := ids.Channels[cfg.IncidentsChannel]
	if !ok {
		return nil, fmt.Errorf("channel %q has no ID in the slack-ids file", cfg.IncidentsChannel)
	}
	ask := map[string]string{}
	for _, name := range cfg.AskChannels {
		id, ok := ids.Channels[name]
		if !ok {
			return nil, fmt.Errorf("channel %q has no ID in the slack-ids file", name)
		}
		ask[id] = name
	}
	return &Escalator{
		cfg: cfg, svc: svc, ids: ids, slack: slack, prom: prom, kube: kube, base: base,
		incidentsID: incidents, askIDs: ask,
		lastEsc: map[string]time.Time{}, lastAsk: map[string]time.Time{}, watching: map[string]bool{},
	}, nil
}

// Wait blocks until every recovery watcher has stopped (after base is cancelled).
func (e *Escalator) Wait() { e.wg.Wait() }

// Check is one criterion of the evidence bar.
type Check struct {
	Name   string `json:"name" jsonschema:"the criterion"`
	Passed bool   `json:"passed" jsonschema:"whether it passed"`
	Detail string `json:"detail" jsonschema:"what was measured"`
}

// EscalateInput is escalate_incident's input.
type EscalateInput struct {
	ImpactedService  string `json:"impacted_service" jsonschema:"the failing service, a catalog service name such as checkout"`
	SuspectedService string `json:"suspected_service" jsonschema:"the dependency you suspect caused it, a catalog service name such as payments"`
	Summary          string `json:"summary" jsonschema:"one line: the symptom and the suspected change, at most 300 characters"`
	AlertThreadLink  string `json:"alert_thread_link,omitempty" jsonschema:"https link to the alert's Slack thread"`
	DryRun           bool   `json:"dry_run,omitempty" jsonschema:"evaluate the evidence only; post nothing"`
}

// EscalateOutput is escalate_incident's result.
type EscalateOutput struct {
	Passed  bool    `json:"passed" jsonschema:"whether every check passed"`
	Posted  bool    `json:"posted" jsonschema:"whether the escalation was posted to #incidents"`
	DryRun  bool    `json:"dry_run" jsonschema:"whether this was a dry run"`
	Checks  []Check `json:"checks" jsonschema:"each criterion and what was measured"`
	Channel string  `json:"channel,omitempty" jsonschema:"where the escalation was posted"`
	TS      string  `json:"ts,omitempty" jsonschema:"the Slack ts of the escalation"`
	Note    string  `json:"note,omitempty" jsonschema:"what to do next when nothing was posted"`
}

// evidence is what the checks measured, for the message.
type evidence struct {
	impactedRatio float64
	depRatio      float64
	suspectRatio  float64
	version       string
	rolledOut     time.Time
	onset         time.Time
}

// Escalate checks the evidence and, unless it is a dry run, posts the escalation when every check
// passes. A failed check is a result, not an error: the caller sees which checks failed and why.
func (e *Escalator) Escalate(ctx context.Context, in EscalateInput) (EscalateOutput, error) {
	impacted, err := e.service(in.ImpactedService)
	if err != nil {
		return EscalateOutput{}, err
	}
	suspect, err := e.service(in.SuspectedService)
	if err != nil {
		return EscalateOutput{}, err
	}
	if impacted.Service == suspect.Service {
		return EscalateOutput{}, errors.New("impacted_service and suspected_service must differ")
	}
	summary := pager.CleanSummary(in.Summary)
	if summary == "" {
		return EscalateOutput{}, errors.New("summary is empty")
	}
	checks, ev := e.evaluate(ctx, impacted.Service, suspect.Service)
	out := EscalateOutput{Passed: true, DryRun: in.DryRun, Checks: checks}
	for _, c := range checks {
		out.Passed = out.Passed && c.Passed
	}
	if in.DryRun {
		return out, nil
	}
	if !out.Passed {
		out.Note = "Nothing was posted: the evidence does not meet the bar. Ask the on-call for help with ask_oncall, saying what you checked and ruled out."
		return out, nil
	}
	key := impacted.Service
	if err := e.reserve(e.lastEsc, key, e.cfg.EscalationWindow, "an escalation for "+key); err != nil {
		return out, err
	}
	link, _ := pager.CleanLink(in.AlertThreadLink)
	text := EscalationMessage(impacted, suspect, ev, summary, link)
	ts, err := e.slack.PostMessage(ctx, e.incidentsID, text)
	if err != nil {
		e.release(e.lastEsc, key)
		return out, fmt.Errorf("post escalation: %w", err)
	}
	out.Posted, out.Channel, out.TS = true, "#"+e.cfg.IncidentsChannel, ts
	e.watch(impacted.Service, ts)
	return out, nil
}

// service validates a model-given service name against the catalog.
func (e *Escalator) service(name string) (catalog.Service, error) {
	s, err := e.svc.Service(name)
	if err != nil {
		return catalog.Service{}, err
	}
	if !nameRE.MatchString(s.Service) {
		return catalog.Service{}, fmt.Errorf("service name %q cannot be used in a query", s.Service)
	}
	return s, nil
}

// evaluate runs every check; a check whose query fails fails with the error as its detail.
func (e *Escalator) evaluate(ctx context.Context, impacted, suspect string) ([]Check, evidence) {
	now := e.cfg.Now()
	var ev evidence
	var checks []Check

	// 1. The impacted service is failing now.
	c := Check{Name: "impacted_failing"}
	if v, ok, err := e.prom.Query(ctx, ratioQuery(e.cfg.Namespace, impacted), now); err != nil {
		c.Detail = "query failed: " + err.Error()
	} else if !ok {
		c.Detail = impacted + " has no traffic to measure"
	} else {
		ev.impactedRatio = v
		c.Passed = v > ImpactedFailingRatio
		c.Detail = fmt.Sprintf("%s 5xx ratio %s (bar: above %s)", impacted, pct(v), pct(ImpactedFailingRatio))
	}
	checks = append(checks, c)

	// 2. The suspect rolled out shortly before the errors began.
	c = Check{Name: "recent_rollout"}
	onset, onsetErr := e.onset(ctx, impacted, now)
	version, rolledOut, rsErr := e.currentRollout(ctx, suspect)
	switch {
	case onsetErr != nil:
		c.Detail = "could not find when the errors began: " + onsetErr.Error()
	case rsErr != nil:
		c.Detail = "could not read " + suspect + "'s rollout: " + rsErr.Error()
	default:
		ev.onset, ev.version, ev.rolledOut = onset, version, rolledOut
		lead := onset.Sub(rolledOut)
		c.Passed = lead <= RolloutBefore && lead >= -RolloutAfter
		c.Detail = fmt.Sprintf("%s %s rolled out %s; %s's errors began %s (%s) (bar: rollout within %s before the errors)",
			suspect, version, clock(rolledOut), impacted, clock(onset), relative(lead), RolloutBefore)
	}
	checks = append(checks, c)

	// 3. The impacted service's calls to the suspect are failing.
	c = Check{Name: "dependency_calls_failing"}
	if q, ok := e.cfg.DependencyQueries[impacted+"->"+suspect]; !ok {
		c.Detail = fmt.Sprintf("no client metric for %s → %s calls in this demo", impacted, suspect)
	} else if v, ok, err := e.prom.Query(ctx, fmt.Sprintf(q, e.cfg.Namespace), now); err != nil {
		c.Detail = "query failed: " + err.Error()
	} else if !ok {
		c.Detail = fmt.Sprintf("%s makes no calls to %s to measure", impacted, suspect)
	} else {
		ev.depRatio = v
		c.Passed = v > DependencyFailingRatio
		c.Detail = fmt.Sprintf("%s → %s calls failing %s (bar: above %s)", impacted, suspect, pct(v), pct(DependencyFailingRatio))
	}
	checks = append(checks, c)

	// 4. The suspect itself looks healthy: the failure is in the contract, not in the suspect.
	c = Check{Name: "suspect_own_errors_low"}
	if v, ok, err := e.prom.Query(ctx, ratioQuery(e.cfg.Namespace, suspect), now); err != nil {
		c.Detail = "query failed: " + err.Error()
	} else if !ok {
		c.Detail = suspect + " has no traffic to measure"
	} else {
		ev.suspectRatio = v
		c.Passed = v < SuspectHealthyRatio
		c.Detail = fmt.Sprintf("%s's own 5xx ratio %s (bar: below %s)", suspect, pct(v), pct(SuspectHealthyRatio))
	}
	checks = append(checks, c)
	return checks, ev
}

// ratioQuery is a service's 5xx ratio over the last minute, probes left out; 0, not absent, when
// it serves no 5xx.
func ratioQuery(ns, app string) string {
	sel := fmt.Sprintf(`namespace=%q,app=%q,handler!~%q`, ns, app, probeHandlers)
	return fmt.Sprintf(`(sum(rate(http_requests_total{%s,code=~"5.."}[1m])) or vector(0)) / sum(rate(http_requests_total{%s}[1m]))`, sel, sel)
}

// onset is when the latest run of errors began: the first sample above the failing bar after the
// ratio had been at or under it for longer than onsetGap. Shorter dips, which a 1m rate shows while
// errors are still flowing, belong to the same run.
func (e *Escalator) onset(ctx context.Context, impacted string, now time.Time) (time.Time, error) {
	samples, err := e.prom.QueryRange(ctx, ratioQuery(e.cfg.Namespace, impacted), now.Add(-onsetLookback), now, onsetStep)
	if err != nil {
		return time.Time{}, err
	}
	var onset, lastAbove time.Time
	for _, s := range samples {
		if s.Value <= ImpactedFailingRatio {
			continue
		}
		if lastAbove.IsZero() || s.Time.Sub(lastAbove) > onsetGap {
			onset = s.Time
		}
		lastAbove = s.Time
	}
	if onset.IsZero() {
		return time.Time{}, fmt.Errorf("%s's 5xx ratio was not above %s in the last %s", impacted, pct(ImpactedFailingRatio), onsetLookback)
	}
	return onset, nil
}

// currentRollout returns the version and creation time of the suspect's current ReplicaSet. A
// rollback to an older ReplicaSet keeps that ReplicaSet's creation time, so this reads a rollback
// as an old rollout; the demo's bad rollout always creates a new ReplicaSet (scripts/reset.sh
// recreates the Deployment).
func (e *Escalator) currentRollout(ctx context.Context, name string) (string, time.Time, error) {
	d, err := e.kube.AppsV1().Deployments(e.cfg.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", time.Time{}, err
	}
	rev := d.Annotations["deployment.kubernetes.io/revision"]
	if rev == "" {
		return "", time.Time{}, fmt.Errorf("deployment %s has no revision yet", name)
	}
	rss, err := e.kube.AppsV1().ReplicaSets(e.cfg.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", time.Time{}, err
	}
	for i := range rss.Items {
		rs := &rss.Items[i]
		if ownedBy(rs, d) && rs.Annotations["deployment.kubernetes.io/revision"] == rev {
			return versionOf(rs), rs.CreationTimestamp.UTC(), nil
		}
	}
	return "", time.Time{}, fmt.Errorf("no ReplicaSet of %s at revision %s", name, rev)
}

func ownedBy(rs *appsv1.ReplicaSet, d *appsv1.Deployment) bool {
	for _, o := range rs.OwnerReferences {
		if o.Kind == "Deployment" && o.Name == d.Name && (d.UID == "" || o.UID == d.UID) {
			return true
		}
	}
	return false
}

func versionOf(rs *appsv1.ReplicaSet) string {
	if v := rs.Spec.Template.Labels["app.kubernetes.io/version"]; v != "" {
		return v
	}
	for _, c := range rs.Spec.Template.Spec.Containers {
		if i := strings.LastIndex(c.Image, ":"); i >= 0 && !strings.Contains(c.Image[i:], "/") {
			return c.Image[i+1:]
		}
	}
	return "revision " + rs.Annotations["deployment.kubernetes.io/revision"]
}

// EscalationMessage builds the escalation post. summary and link must already be cleaned; the
// service names come from the catalog.
func EscalationMessage(impacted, suspect catalog.Service, ev evidence, summary, link string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Escalation · %s failing (%s of requests 5xx)\n", impacted.Service, pct(ev.impactedRatio))
	fmt.Fprintf(&b, "Suspected cause: %s %s, rolled out %s, %s before the errors began (owner: %s)\n",
		suspect.Service, pager.Escape(ev.version), clock(ev.rolledOut), ev.onset.Sub(ev.rolledOut).Round(time.Second), suspect.Team)
	fmt.Fprintf(&b, "Evidence: %s → %s calls failing %s · %s 5xx %s\n",
		impacted.Service, suspect.Service, pct(ev.depRatio), suspect.Service, pct(ev.suspectRatio))
	b.WriteString(summary)
	if link != "" {
		b.WriteString(" · ")
		b.WriteString(link)
	}
	return b.String()
}

// ResolvedMessage builds the resolution post.
func ResolvedMessage(impacted string, since time.Time, permalink string) string {
	msg := fmt.Sprintf("Resolved · %s error rate under %s since %s", impacted, pct(ResolvedRatio), clock(since))
	if link, ok := pager.CleanLink(permalink); ok {
		msg += " · escalation: " + link
	}
	return msg
}

// watch starts the recovery watcher for impacted, unless one is running.
func (e *Escalator) watch(impacted, escalationTS string) {
	e.mu.Lock()
	if e.watching[impacted] {
		e.mu.Unlock()
		return
	}
	e.watching[impacted] = true
	e.mu.Unlock()
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer func() {
			e.mu.Lock()
			delete(e.watching, impacted)
			e.mu.Unlock()
		}()
		e.runWatch(impacted, escalationTS)
	}()
}

func (e *Escalator) runWatch(impacted, escalationTS string) {
	ctx, cancel := context.WithTimeout(e.base, e.cfg.MaxWatch)
	defer cancel()
	log := e.cfg.Logger.With("watch", impacted)
	ticker := time.NewTicker(e.cfg.PollEvery)
	defer ticker.Stop()
	var below time.Time
	for {
		select {
		case <-ctx.Done():
			log.Info("recovery watch ended without recovery", "reason", ctx.Err())
			return
		case <-ticker.C:
		}
		now := e.cfg.Now()
		v, ok, err := e.prom.Query(ctx, ratioQuery(e.cfg.Namespace, impacted), now)
		switch {
		case err != nil:
			log.Warn("recovery watch query failed", "err", err)
			continue
		case !ok || v >= ResolvedRatio:
			below = time.Time{}
			continue
		case below.IsZero():
			below = now
		}
		if now.Sub(below) < ResolvedFor {
			continue
		}
		permalink, err := e.slack.Permalink(ctx, e.incidentsID, escalationTS)
		if err != nil {
			log.Warn("permalink of the escalation failed", "err", err)
		}
		if _, err := e.slack.PostMessage(ctx, e.incidentsID, ResolvedMessage(impacted, below, permalink)); err != nil {
			log.Error("post resolved failed", "err", err)
			return
		}
		log.Info("posted resolved", "since", below)
		return
	}
}

// AskInput is ask_oncall's input.
type AskInput struct {
	Team            string `json:"team" jsonschema:"the team whose on-call to ask, a catalog team name such as checkout"`
	Summary         string `json:"summary" jsonschema:"what you checked, what you ruled out and what you need, at most 300 characters"`
	AlertThreadLink string `json:"alert_thread_link" jsonschema:"https link to the alert's Slack thread; the question is posted as a reply there"`
}

// AskOutput is ask_oncall's result.
type AskOutput struct {
	Posted  bool   `json:"posted" jsonschema:"whether the question was posted"`
	Channel string `json:"channel" jsonschema:"the channel it was posted in"`
	TS      string `json:"ts" jsonschema:"the Slack ts of the reply"`
}

// AskOncall replies in the alert thread, mentioning the team's on-call.
func (e *Escalator) AskOncall(ctx context.Context, in AskInput) (AskOutput, error) {
	summary := pager.CleanSummary(in.Summary)
	if summary == "" {
		return AskOutput{}, errors.New("summary is empty")
	}
	channel, thread, err := ParseThreadLink(in.AlertThreadLink)
	if err != nil {
		return AskOutput{}, err
	}
	name, ok := e.askIDs[channel]
	if !ok {
		return AskOutput{}, fmt.Errorf("ask_oncall may only reply in %s", strings.Join(e.cfg.AskChannels, ", "))
	}
	team := strings.ToLower(strings.TrimSpace(in.Team))
	ids, ok := e.ids.Teams[team]
	if !ok {
		return AskOutput{}, fmt.Errorf("team %q has no Slack IDs configured; nothing was posted", in.Team)
	}
	key := channel + "/" + thread
	if err := e.reserve(e.lastAsk, key, e.cfg.AskWindow, "a question in this thread"); err != nil {
		return AskOutput{}, err
	}
	ts, err := e.slack.PostReply(ctx, channel, thread, AskMessage(ids, summary))
	if err != nil {
		e.release(e.lastAsk, key)
		return AskOutput{}, fmt.Errorf("post question: %w", err)
	}
	return AskOutput{Posted: true, Channel: "#" + name, TS: ts}, nil
}

// AskMessage builds the question. summary must already be cleaned.
func AskMessage(ids slackids.Team, summary string) string {
	who := fmt.Sprintf("<@%s>", ids.PrimaryUserID)
	if ids.OncallGroupID != "" {
		who = fmt.Sprintf("<!subteam^%s> <@%s>", ids.OncallGroupID, ids.PrimaryUserID)
	}
	return who + " the agent in this thread could not confirm the cause and needs a person: " + summary
}

var archivePath = regexp.MustCompile(`^/archives/([A-Z0-9]{2,32})/p(\d{10})(\d{6})$`)
var tsRE = regexp.MustCompile(`^\d{10}\.\d{6}$`)

// ParseThreadLink returns the channel and thread ts of a Slack message link. A reply's link
// carries its thread in the thread_ts query parameter.
func ParseThreadLink(link string) (channel, threadTS string, err error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return "", "", fmt.Errorf("alert_thread_link %q is not an https Slack link", link)
	}
	host := strings.ToLower(u.Hostname())
	if host != "slack.com" && !strings.HasSuffix(host, ".slack.com") {
		return "", "", fmt.Errorf("alert_thread_link %q is not a Slack link", link)
	}
	m := archivePath.FindStringSubmatch(u.Path)
	if m == nil {
		return "", "", fmt.Errorf("alert_thread_link %q is not a Slack message link (/archives/<channel>/p<ts>)", link)
	}
	channel, threadTS = m[1], m[2]+"."+m[3]
	if t := u.Query().Get("thread_ts"); t != "" {
		if !tsRE.MatchString(t) {
			return "", "", fmt.Errorf("alert_thread_link has a malformed thread_ts %q", t)
		}
		threadTS = t
	}
	return channel, threadTS, nil
}

// reserve claims key in m for window, or fails if it was claimed within it.
func (e *Escalator) reserve(m map[string]time.Time, key string, window time.Duration, what string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.cfg.Now()
	if last, ok := m[key]; ok && now.Sub(last) < window {
		return fmt.Errorf("%s was already posted %s ago; at most one per %s", what, now.Sub(last).Round(time.Second), window)
	}
	m[key] = now
	return nil
}

func (e *Escalator) release(m map[string]time.Time, key string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(m, key)
}

func pct(v float64) string {
	return fmt.Sprintf("%.1f%%", v*100)
}

func clock(t time.Time) string { return t.UTC().Format("15:04:05 UTC") }

// relative says how long after (or before) the rollout the errors began.
func relative(d time.Duration) string {
	d = d.Round(time.Second)
	if d >= 0 {
		return d.String() + " after the rollout"
	}
	return (-d).String() + " before the rollout"
}
