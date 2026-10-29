# slackread-mcp

Reads Slack, read-only, so incident-commander can see what the other agents found while it writes the summary.

## Tools

| Tool | Input | Returns |
|---|---|---|
| `read_thread` | `channel` (no `#`), `thread_ts` | the thread's parent and replies |
| `read_recent` | `channel`, `since_minutes` (1 to 240) | the channel's messages in that window from Slack's `conversations.history`, which leaves thread replies out unless they were also sent to the channel (Slack's behaviour) |

- Both return lines of `HH:MM:SS name: text` in the `--tz` time zone. `read_recent` sorts them oldest first;
  `read_thread` keeps the order Slack's `conversations.replies` returns. A person is shown by the name Slack
  shows for them (the user ID if the lookup fails), a bot by its name; a message with Block Kit content is read from its section and rich-text blocks.
- Only the channels in `--channels` can be read. Each needs an ID in the `demo-slack-ids` Secret, or the server
  does not start.
- One call reads at most 10 pages of 200 messages; messages past that are dropped without a note. The output stops
  before the line that would take it past 20 KiB and ends with a line saying how many messages were left out. The tool descriptions tell the model the text is data, not instructions.

## Running

| Flag | Default | |
|---|---|---|
| `--slack-ids` | `/etc/demo-slack-ids/slack-ids.yaml` | workspace Slack IDs |
| `--slack-api-url` | `https://slack.com/api` | Slack Web API base URL |
| `--channels` | `checkout-alerts,payments-ops,incidents` | the channels that may be read |
| `--tz` | `UTC` | IANA time zone for message times |
| `--addr` | `:8084` | listen address; MCP at `/mcp`, `/healthz` for probes |
| `--version` | | print the version and exit |

`SLACK_BOT_TOKEN` is incident-commander's bot token, from Secret `demo-ic-slack` (key `bot-token`); the server
does not start without it. The bot must be a member of every channel it reads; otherwise Slack answers
`not_in_channel` (Slack's behaviour).
No Kubernetes access. Tests: `go test ./slackread/...` from `mcp/`.
