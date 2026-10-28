# Observability for the incident demo

Metrics, traces, dashboards and the `CheckoutHighErrorRate` alert for the `checkout` and `payments` services in namespace
`commerce`, on the kind cluster `agent-kourier-full`. The Alertmanager posts the alert to `#checkout-alerts`, where Agent
Kourier picks it up.

```sh
observability/install.sh          # install or update; safe to re-run
observability/scripts/check.sh    # offline: promtool and amtool on the rendered config (Docker, helm, yq)
observability/scripts/check.sh --cluster   # also the operator-generated Alertmanager config on the cluster
observability/scripts/verify.sh   # on the cluster: targets, rules, routes, a trace into Tempo, Grafana, memory
observability/alertmanager/restore.sh      # give the Alertmanager its previous configuration back
```

`KUBE_CONTEXT` selects the cluster (default `kind-agent-kourier-full`).

## What is installed

| Namespace | What | Chart |
|---|---|---|
| `observability` | Prometheus server + kube-state-metrics (no volume, retention 6h, scrape 15s) | prometheus-community/prometheus 29.39.0 |
| `observability` | Tempo, single binary, no volume, OTLP gRPC :4317 and HTTP :4318, metrics generator (service graph, span metrics) | grafana/tempo 1.24.4 |
| `observability` | OpenTelemetry collector (contrib image), Deployment: OTLP in, `memory_limiter`, `k8sattributes`, `batch`, to Tempo | open-telemetry/opentelemetry-collector 0.178.0 |
| `observability` | Grafana, no volume, no anonymous access, datasources Prometheus and Tempo, dashboard Commerce | grafana/grafana 10.5.15 |
| `demo-tools` | Secret `demo-grafana-token` (`token`, `url`): a Viewer service account token (`kourier-mcp`) for an MCP server | |
| `commerce` | the namespace only | |
| `monitoring` | the Alertmanager `agent-kourier` gets the base configuration `alertmanager/config.yaml` | |

Services send traces to `otel-collector.observability.svc:4317`. Prometheus scrapes every pod in `commerce` labelled
`app.kubernetes.io/part-of: commerce` on `:8080/metrics`, with the labels `app` (`app.kubernetes.io/name`) and `version`
(`app.kubernetes.io/version`). The Grafana admin login is in the Secret `observability/grafana-admin`, generated once:

```sh
kubectl --context kind-agent-kourier-full -n observability port-forward svc/grafana 3000:80
```

## The alert

`values/prometheus-rules.yaml`: `CheckoutHighErrorRate` fires when more than 5% of checkout's requests return a 5xx over 1m
(the handlers `/healthz`, `/readyz`, `/livez` and `/metrics` are left out), held for 1m, with the labels
`severity=critical`, `team=checkout`, `service=checkout`, `namespace=commerce`. `tests/rules.test.yaml` tests it with promtool.

The Alertmanager routes it, and any alert with `team=checkout` and `severity=critical`, to the receiver `checkout-alerts`
(group by `alertname`, `namespace`; wait 10s, interval 30s, repeat 4h; resolved messages on). The Slack title is
`FIRING (<n>) - <alertname>` or `RESOLVED - <alertname>`, and the title link is
`<externalUrl>/#/alerts?receiver=checkout-alerts`. The e2e sink's route (the AlertmanagerConfig `monitoring/sink`) still
comes first, with `continue: true`.

The webhook URL is read from `~/.config/agent-kourier/demo-checkout-alerts-webhook-url` (one line, mode 600) into the Secret
`monitoring/demo-checkout-alerts-slack`, which the Alertmanager mounts and reads with `api_url_file`; it is never printed or
committed. Without that file, `install.sh` installs the routes with a receiver that posts nothing and says what is missing;
add the file and run `install.sh` again.

## Restoring the Alertmanager

`install.sh` sets three fields of the Alertmanager, `configSecret`, `secrets` and `externalUrl`, by a JSON merge patch, and
first saves their previous values (and the base configuration they named) in the Secret
`monitoring/demo-alertmanager-previous`. `alertmanager/previous-state.json` records the state before the first install:
all three unset. To restore:

```sh
observability/alertmanager/restore.sh
```

which, for that state, comes down to:

```sh
kubectl --context kind-agent-kourier-full -n monitoring patch alertmanager agent-kourier --type=merge \
  -p '{"spec":{"configSecret":null,"secrets":null,"externalUrl":null}}'
# once the operator has rolled the pod:
kubectl --context kind-agent-kourier-full -n monitoring delete secret \
  alertmanager-demo-config demo-checkout-alerts-slack demo-alertmanager-previous --ignore-not-found
```

Do not change this Alertmanager with `kubectl apply --server-side`: it was created by a client-side `kubectl apply`, and a
server-side apply under another field manager takes over that apply's fields and deletes the ones its manifest leaves out
(the AlertmanagerConfig selectors, so the e2e sink's route disappears).
