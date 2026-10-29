# catalog-mcp

The software catalog, read-only: who owns a service, where that team talks, who is on call. All three agents
use it to find the team behind a service.

## Tools

| Tool | Input | Returns |
|---|---|---|
| `lookup_service` | `name` | the service's system, owning team and its display name, the team's Slack channel, primary and secondary on-call, GitHub repository and tier. An unknown name is an error that lists the known services. |
| `list_services` | none | every service with its owning team |

Both are read-only.

## Data

At startup the server reads every YAML file in `--catalog-dir` except `*.example.yaml`, `slack-ids*` and
`kustomization*`: Backstage `Component` entities are services,
`Group` entities are teams, and other kinds (the payments `API`) are skipped. It does not re-read the files.

The files are the repository's [`catalog/`](../../catalog), packed into the ConfigMap `demo-catalog` with a
content hash in its name, so changing the catalog and running `make tools` rolls every server that mounts it
(catalog, pager, escalation). Workspace Slack IDs are not in the catalog; they are in the `demo-slack-ids` Secret.

## Running

| Flag | Default | |
|---|---|---|
| `--catalog-dir` | `/catalog` | the catalog directory |
| `--addr` | `:8084` | listen address; MCP at `/mcp`, `/healthz` for probes |
| `--version` | | print the version and exit |

No Secret and no Kubernetes access. Tests: `go test ./catalog/...` from `mcp/`.
