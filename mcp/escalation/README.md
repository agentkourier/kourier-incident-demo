# escalation-mcp

Hands an incident from `#checkout-alerts` to `#incidents` without a person relaying it, and pulls a person in when
the cause can't be confirmed. checkout-sre calls it; it posts as the Pager app.

## Who decides the agent is sure

Not the model. `escalate_incident` re-checks the evidence itself, with Prometheus and Kubernetes queries, and
posts to `#incidents` only when every check passes:

| Check | Passes when |
|---|---|
| `impacted_failing` | the impacted service's 5xx ratio over the last minute is above 5% |
| `recent_rollout` | the suspected service's current ReplicaSet was created between 15 minutes before and 1 minute after the impacted service's errors began |
| `dependency_calls_failing` | more than 50% of the impacted service's calls to the suspected service fail |
| `suspect_own_errors_low` | the suspected service's own 5xx ratio is under 5% |

- The 5xx ratios read `http_requests_total` in `--allowed-namespace`, leaving out `/healthz`, `/readyz`, `/livez`
  and `/metrics`. When the errors began is the first sample above 5% after the ratio had been at or under it for
  more than a minute, looking back up to an hour.
- The third check knows only the checkout → payments client metric (`payments_client_requests_total`); for any
  other pair it fails and says there is no metric.
- The result lists each check with what it measured. A failed check is a result, not an error, and nothing is
  posted. `dry_run` runs the checks and posts nothing.
- The alert text an agent reads is untrusted, so a crafted annotation can't talk an agent into escalating. These
  thresholds are this demo's policy, constants in [`escalation.go`](escalation.go).

## Tools

| Tool | Input | Does |
|---|---|---|
| `escalate_incident` | `impacted_service`, `suspected_service`, `summary`, optional `alert_thread_link`, `dry_run` | the checks above; when they pass, posts the escalation to `--incidents-channel` |
| `ask_oncall` | `team`, `summary`, `alert_thread_link` | replies in the alert's thread, mentioning the team's on-call group and primary on-call |

- Both services must be in the catalog and differ. The tool writes every message: `summary` is cleaned the way
  `page_team` cleans it, and mentions come only from the `demo-slack-ids` Secret.
- `ask_oncall` replies only in threads of `--ask-channels`, and its link must be a Slack message link
  (`https://….slack.com/archives/<channel>/p<ts>`).
- At most one escalation per impacted service every 30 minutes, and one question per thread every 10 minutes.
- After an escalation it posts, the server watches the impacted service every 15 seconds and posts "Resolved" to
  `#incidents` once its 5xx ratio has stayed under 1% for 2 minutes. It stops watching after an hour.
- The limits and the watchers live in memory: a restart forgets them, and a watcher running at the time is lost.

## Running

| Flag | Default | |
|---|---|---|
| `--catalog-dir` | `/catalog` | the catalog directory |
| `--slack-ids` | `/etc/demo-slack-ids/slack-ids.yaml` | workspace Slack IDs |
| `--slack-api-url` | `https://slack.com/api` | Slack Web API base URL |
| `--allowed-namespace` | `commerce` | the only namespace the checks read |
| `--prometheus-url` | `http://prometheus-server.observability.svc` | Prometheus base URL |
| `--incidents-channel` | `incidents` | where escalations and resolutions are posted |
| `--ask-channels` | `checkout-alerts` | comma-separated channels `ask_oncall` may reply in |
| `--addr` | `:8084` | listen address; MCP at `/mcp`, `/healthz` for probes |
| `--version` | | print the version and exit |

`SLACK_BOT_TOKEN` is the Pager app's bot token, from Secret `demo-pager-slack` (key `bot-token`); the server
does not start without it. It runs with ServiceAccount `escalation-mcp`, whose only grant is a Role in `commerce`:
get and list Deployments and ReplicaSets ([`deploy/rbac.yaml`](deploy/rbac.yaml)). Prometheus is reached without
credentials. Tests: `go test ./escalation/...` from `mcp/`.
