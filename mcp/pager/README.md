# pager-mcp

Pages a team's on-call in Slack, as the Pager app. incident-commander's prompt has it page the owning team once it
has declared an incident.

## Tool

`page_team` (`team`, `severity` 1 to 4, `summary`, optional `incident_link`) posts one top-level message to the
team's Slack channel:

```
SEV2 for @payments-oncall → paging @Dana (primary on-call)
Escalates to @Lee (secondary) if not acknowledged in 5 min.
*Summary:* <summary>
Alert thread
```

- The tool writes the message. Mentions and the channel come only from the `demo-slack-ids` Secret; a team without
  IDs there is an error and nothing is posted. The on-call group is mentioned only when the Secret has one, and the
  second line appears only when it names a secondary. The second line is text only: nothing in the demo escalates.
- `summary` comes from a model and is treated as untrusted: control and format characters are stripped, it is cut
  to 300 characters, and `&`, `<` and `>` are escaped, so it can't mention anyone.
- `incident_link` is kept only when it is an https link on `slack.com`; otherwise it is left out and the result
  says `link_dropped`.
- At most one page per team every 10 minutes. The limit is held in memory, so a restart forgets it; a post that
  fails frees the slot again.

## Running

| Flag | Default | |
|---|---|---|
| `--catalog-dir` | `/catalog` | the catalog directory, to find the team |
| `--slack-ids` | `/etc/demo-slack-ids/slack-ids.yaml` | workspace Slack IDs |
| `--slack-api-url` | `https://slack.com/api` | Slack Web API base URL |
| `--addr` | `:8084` | listen address; MCP at `/mcp`, `/healthz` for probes |
| `--version` | | print the version and exit |

`SLACK_BOT_TOKEN` is the Pager app's bot token, from Secret `demo-pager-slack` (key `bot-token`); the server
does not start without it. Pager has `chat:write` and `chat:write.public`; with the second, Slack lets it post in
public channels it hasn't joined (Slack's behaviour). No Kubernetes
access. Tests: `go test ./pager/...` from `mcp/`.
