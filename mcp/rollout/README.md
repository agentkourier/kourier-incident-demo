# rollout-mcp

Reads a Deployment's rollout history and rolls it back, the way `kubectl rollout history` and
`kubectl rollout undo --to-revision` do. payments-sre uses it to name the bad revision and undo it.

## Tools

| Tool | Input | Does |
|---|---|---|
| `rollout_history` | `namespace`, `deployment` | lists the revisions, oldest first, with each one's image, version, creation time, replica count and whether it is current. Read-only. |
| `rollback_deployment` | `namespace`, `deployment`, `to_revision`, optional `dry_run` | writes the chosen revision's pod template back to the Deployment |

- Both refuse any namespace but `--allowed-namespace`. `rollout_history` reads any Deployment there;
  `rollback_deployment` only the ones in `--allowed-deployments`.
- `rollback_deployment` refuses a paused Deployment and a revision that does not exist, and changes nothing when
  the revision is already the current template. `dry_run` reports what would change.
- The update carries the Deployment's resourceVersion, so a concurrent change makes the rollback fail instead of
  being overwritten.
- The approval is not here. payments-sre's `agents.yaml` entry marks `rollback_deployment` `requireApproval`, and
  Agent Kourier lets only the Binding's approvers (`@payments-team`) approve it.

## Running

| Flag | Default | |
|---|---|---|
| `--allowed-namespace` | `commerce` | the only namespace the tools read |
| `--allowed-deployments` | `payments` | comma-separated Deployments that may be rolled back |
| `--addr` | `:8084` | listen address; MCP at `/mcp`, `/healthz` for probes |
| `--version` | | print the version and exit |

Runs in the cluster with ServiceAccount `rollout-mcp`, whose only grant is a Role in `commerce`: get, list and
watch Deployments and ReplicaSets, and patch and update Deployments ([`deploy/rbac.yaml`](deploy/rbac.yaml)).
No Secret. Tests: `go test ./rollout/...` from `mcp/`.
