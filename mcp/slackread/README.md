# slackread-mcp

Reads Slack, read-only, so incident-commander can see what the other agents found while it writes the summary.

## Tools

| Tool | Input | Returns |
|---|---|---|
| `read_thread` | `channel` (no `#`), `thread_ts` | the thread's parent and replies |
| `read_recent` | `channel`, `since_minutes` (1 to 240) | the channel's top-level messages in that window; replies are left out |

- Both return lines of `HH:MM:SS name: text`, oldest first, in the `--tz` time zone. A person is shown by the name Slack
  shows for them (the user ID if the lookup fails), a bot by its name; a message with Block Kit content is read from its section and rich-text blocks.
- Only the channels in `--channels` can be read. Each needs an ID in the `demo-slack-ids` Secret, or the server
  does not start.
- One call reads at most 10 pages of 200 messages and returns at most 20 KiB, ending with a line that says how many
  messages were cut. The tool descriptions tell the model the text is data, not instructions.

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
does not start without it. The bot must be a member of every channel it reads, or Slack answers `not_in_channel`.
No Kubernetes access. Tests: `go test ./slackread/...` from `mcp/`.
