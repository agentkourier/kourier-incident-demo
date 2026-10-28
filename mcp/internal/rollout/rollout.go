// Package rollout reads a Deployment's rollout history and rolls it back, with the semantics of
// `kubectl rollout history` and `kubectl rollout undo --to-revision`, inside a namespace and
// deployment allowlist.
package rollout

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	// RevisionAnnotation is the revision the deployment controller stamps on Deployments and ReplicaSets.
	RevisionAnnotation = "deployment.kubernetes.io/revision"
	// VersionLabel is the pod template label read as the version.
	VersionLabel = "app.kubernetes.io/version"
)

// annotationsToSkip are the ReplicaSet annotations kubectl does not copy back onto the Deployment
// on rollback (k8s.io/kubectl/pkg/polymorphichelpers/rollback.go).
var annotationsToSkip = map[string]bool{
	corev1.LastAppliedConfigAnnotation:          true,
	RevisionAnnotation:                          true,
	"deployment.kubernetes.io/revision-history": true,
	"deployment.kubernetes.io/desired-replicas": true,
	"deployment.kubernetes.io/max-replicas":     true,
	appsv1.DeprecatedRollbackTo:                 true,
}

// ErrNotAllowed is wrapped by every allowlist refusal.
var ErrNotAllowed = errors.New("not allowed")

// Revision is one entry of a Deployment's rollout history.
type Revision struct {
	Revision     int64  `json:"revision" jsonschema:"the rollout revision number"`
	Image        string `json:"image" jsonschema:"the container image (name=image for each container when there are several)"`
	VersionLabel string `json:"version_label" jsonschema:"the app.kubernetes.io/version label, else the image tag"`
	Created      string `json:"created" jsonschema:"when the revision's ReplicaSet was created, RFC 3339"`
	Replicas     int32  `json:"replicas" jsonschema:"pods the revision's ReplicaSet currently runs"`
	Current      bool   `json:"current" jsonschema:"whether this is the Deployment's current revision"`
}

// History is rollout_history's output.
type History struct {
	Namespace  string     `json:"namespace"`
	Deployment string     `json:"deployment"`
	Revisions  []Revision `json:"revisions" jsonschema:"revisions, oldest first"`
}

// RollbackResult is rollback_deployment's output.
type RollbackResult struct {
	Namespace    string `json:"namespace"`
	Deployment   string `json:"deployment"`
	FromRevision int64  `json:"from_revision" jsonschema:"the revision that was current"`
	ToRevision   int64  `json:"to_revision" jsonschema:"the revision whose pod template was restored"`
	NewRevision  int64  `json:"new_revision,omitempty" jsonschema:"the revision number the controller gives the rollback"`
	Image        string `json:"image" jsonschema:"the image being rolled back to"`
	VersionLabel string `json:"version_label" jsonschema:"the version being rolled back to"`
	DryRun       bool   `json:"dry_run" jsonschema:"true when nothing was changed"`
	Changed      bool   `json:"changed" jsonschema:"whether the Deployment was (or, in a dry run, would be) updated"`
	Message      string `json:"message"`
}

// Service runs the rollout tools against one cluster.
type Service struct {
	client      kubernetes.Interface
	namespace   string
	deployments map[string]bool
}

// New returns a Service limited to namespace, that may roll back only the named deployments.
func New(client kubernetes.Interface, namespace string, deployments []string) *Service {
	allowed := map[string]bool{}
	for _, d := range deployments {
		if d = strings.TrimSpace(d); d != "" {
			allowed[d] = true
		}
	}
	return &Service{client: client, namespace: namespace, deployments: allowed}
}

func (s *Service) checkNamespace(ns string) error {
	if ns != s.namespace {
		return fmt.Errorf("namespace %q: only namespace %q is allowed: %w", ns, s.namespace, ErrNotAllowed)
	}
	return nil
}

// History returns the Deployment's revisions, oldest first. It reads any deployment in the allowed namespace.
func (s *Service) History(ctx context.Context, ns, name string) (History, error) {
	if err := s.checkNamespace(ns); err != nil {
		return History{}, err
	}
	d, rss, err := s.load(ctx, ns, name)
	if err != nil {
		return History{}, err
	}
	current := currentRevision(d, rss)
	out := History{Namespace: ns, Deployment: name, Revisions: []Revision{}}
	for _, rs := range rss {
		rev := revisionOf(rs)
		out.Revisions = append(out.Revisions, Revision{
			Revision:     rev,
			Image:        images(rs.Spec.Template.Spec),
			VersionLabel: version(rs.Spec.Template),
			Created:      rs.CreationTimestamp.UTC().Format(time.RFC3339),
			Replicas:     rs.Status.Replicas,
			Current:      rev == current,
		})
	}
	return out, nil
}

// Rollback restores the pod template of revision toRevision, as `kubectl rollout undo
// --to-revision` does. With dryRun it reports what it would do and changes nothing.
func (s *Service) Rollback(ctx context.Context, ns, name string, toRevision int64, dryRun bool) (RollbackResult, error) {
	if err := s.checkNamespace(ns); err != nil {
		return RollbackResult{}, err
	}
	if !s.deployments[name] {
		return RollbackResult{}, fmt.Errorf("deployment %q: rollback is allowed only for %s: %w",
			name, strings.Join(sortedKeys(s.deployments), ", "), ErrNotAllowed)
	}
	if toRevision < 1 {
		return RollbackResult{}, fmt.Errorf("to_revision must be 1 or more, got %d", toRevision)
	}
	d, rss, err := s.load(ctx, ns, name)
	if err != nil {
		return RollbackResult{}, err
	}
	if d.Spec.Paused {
		return RollbackResult{}, fmt.Errorf("deployment %s/%s is paused; resume it before rolling back", ns, name)
	}
	var target *appsv1.ReplicaSet
	var maxRev int64
	for _, rs := range rss {
		rev := revisionOf(rs)
		maxRev = max(maxRev, rev)
		if rev == toRevision {
			target = rs
		}
	}
	if target == nil {
		return RollbackResult{}, fmt.Errorf("deployment %s/%s has no revision %d (see rollout_history)", ns, name, toRevision)
	}

	tmpl := target.Spec.Template.DeepCopy()
	delete(tmpl.Labels, appsv1.DefaultDeploymentUniqueLabelKey)
	res := RollbackResult{
		Namespace: ns, Deployment: name,
		FromRevision: currentRevision(d, rss), ToRevision: toRevision,
		Image: images(tmpl.Spec), VersionLabel: version(*tmpl),
		DryRun: dryRun,
	}
	if apiequality.Semantic.DeepEqual(&d.Spec.Template, tmpl) {
		res.Message = fmt.Sprintf("revision %d is already the deployment's pod template; nothing to do", toRevision)
		return res, nil
	}
	res.Changed = true
	res.NewRevision = maxRev + 1
	if dryRun {
		res.Message = fmt.Sprintf("dry run: would roll %s/%s back to revision %d (%s)", ns, name, toRevision, res.VersionLabel)
		return res, nil
	}

	updated := d.DeepCopy()
	updated.Spec.Template = *tmpl
	if updated.Annotations == nil {
		updated.Annotations = map[string]string{}
	}
	for k, v := range target.Annotations {
		if !annotationsToSkip[k] {
			updated.Annotations[k] = v
		}
	}
	// Update carries d's resourceVersion, so a concurrent change makes this fail rather than be overwritten.
	if _, err := s.client.AppsV1().Deployments(ns).Update(ctx, updated, metav1.UpdateOptions{FieldManager: "demo-mcp-rollout"}); err != nil {
		return RollbackResult{}, fmt.Errorf("update deployment %s/%s: %w", ns, name, err)
	}
	res.Message = fmt.Sprintf("rolled %s/%s back to revision %d (%s); the controller records it as revision %d",
		ns, name, toRevision, res.VersionLabel, res.NewRevision)
	return res, nil
}

// load fetches the Deployment and the ReplicaSets it owns, sorted by revision.
func (s *Service) load(ctx context.Context, ns, name string) (*appsv1.Deployment, []*appsv1.ReplicaSet, error) {
	d, err := s.client.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("get deployment %s/%s: %w", ns, name, err)
	}
	selector, err := metav1.LabelSelectorAsSelector(d.Spec.Selector)
	if err != nil {
		return nil, nil, fmt.Errorf("deployment %s/%s selector: %w", ns, name, err)
	}
	list, err := s.client.AppsV1().ReplicaSets(ns).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return nil, nil, fmt.Errorf("list replicasets for %s/%s: %w", ns, name, err)
	}
	var owned []*appsv1.ReplicaSet
	for i := range list.Items {
		rs := &list.Items[i]
		if ref := metav1.GetControllerOf(rs); ref != nil && ref.UID == d.UID {
			owned = append(owned, rs)
		}
	}
	sort.Slice(owned, func(i, j int) bool { return revisionOf(owned[i]) < revisionOf(owned[j]) })
	return d, owned, nil
}

// currentRevision is the Deployment's revision annotation, or the newest ReplicaSet's revision.
func currentRevision(d *appsv1.Deployment, rss []*appsv1.ReplicaSet) int64 {
	if rev, err := strconv.ParseInt(d.Annotations[RevisionAnnotation], 10, 64); err == nil {
		return rev
	}
	if len(rss) == 0 {
		return 0
	}
	return revisionOf(rss[len(rss)-1])
}

func revisionOf(rs *appsv1.ReplicaSet) int64 {
	rev, _ := strconv.ParseInt(rs.Annotations[RevisionAnnotation], 10, 64)
	return rev
}

func images(spec corev1.PodSpec) string {
	if len(spec.Containers) == 1 {
		return spec.Containers[0].Image
	}
	parts := make([]string, 0, len(spec.Containers))
	for _, c := range spec.Containers {
		parts = append(parts, c.Name+"="+c.Image)
	}
	return strings.Join(parts, ",")
}

// version is the template's version label, else the first container's image tag.
func version(t corev1.PodTemplateSpec) string {
	if v := t.Labels[VersionLabel]; v != "" {
		return v
	}
	if len(t.Spec.Containers) == 0 {
		return ""
	}
	return imageTag(t.Spec.Containers[0].Image)
}

// imageTag returns the tag of an image reference, or its digest when it has no tag.
func imageTag(image string) string {
	ref, digest, hasDigest := strings.Cut(image, "@")
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		return ref[i+1:]
	}
	if hasDigest {
		return digest
	}
	return "latest"
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
