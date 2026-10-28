#!/usr/bin/env bash
# Checks the installed stack on the cluster (KUBE_CONTEXT, default kind-agent-kourier-full). Prints no secret.
#   - Prometheus: its own targets are up, the demo's rules are loaded, it sends alerts to the Alertmanager
#   - the Alertmanager runs a configuration with the e2e sink's route and the checkout-alerts route
#   - a synthetic trace (telemetrygen) goes through the collector into Tempo, with the k8sattributes processor's attributes
#   - Grafana: the Commerce dashboard is provisioned and the token in demo-tools/demo-grafana-token works
#   - memory: the kind node and the pods in observability
set -euo pipefail

CTX=${KUBE_CONTEXT:-kind-agent-kourier-full}
NODE=${KIND_NODE:-agent-kourier-full-control-plane}
TELEMETRYGEN_IMAGE=ghcr.io/open-telemetry/opentelemetry-collector-contrib/telemetrygen:v0.163.0
K=(kubectl --context "${CTX}")
NS=observability

fail() {
  echo "FAIL: $*" >&2
  exit 1
}
ok() { echo "ok: $*"; }
# prom_get URL: GET from inside the Prometheus pod (its image has wget), so no port-forward is needed.
prom_get() { "${K[@]}" -n "${NS}" exec deploy/prometheus-server -c prometheus-server -- wget -qO- "$1"; }

# --- Prometheus ---------------------------------------------------------------------------------------------------------
targets=$(prom_get http://localhost:9090/api/v1/targets)
for job in prometheus kube-state-metrics tempo otel-collector; do
  jq -e --arg j "${job}" '[.data.activeTargets[] | select(.labels.job == $j)] | length > 0 and all(.health == "up")' >/dev/null <<<"${targets}" ||
    fail "Prometheus target ${job} is not up"
done
ok "Prometheus targets prometheus, kube-state-metrics, tempo, otel-collector are up"
echo "   commerce targets: $(jq '[.data.activeTargets[] | select(.labels.job == "commerce")] | length' <<<"${targets}") (0 until checkout and payments run)"
rules=$(prom_get http://localhost:9090/api/v1/rules)
jq -e '[.data.groups[].rules[] | select(.name == "CheckoutHighErrorRate" and .health == "ok")] | length == 1' >/dev/null <<<"${rules}" ||
  fail "CheckoutHighErrorRate is not loaded"
ok "rules loaded: $(jq -r '[.data.groups[].rules[].name] | join(", ")' <<<"${rules}")"
prom_get http://localhost:9090/api/v1/alertmanagers |
  jq -e '.data.activeAlertmanagers | map(.url) | index("http://alertmanager-operated.monitoring.svc:9093/api/v2/alerts") != null' >/dev/null ||
  fail "Prometheus has no active Alertmanager"
ok "Prometheus sends alerts to alertmanager-operated.monitoring.svc:9093"

# --- Alertmanager -------------------------------------------------------------------------------------------------------
am=$("${K[@]}" -n monitoring exec alertmanager-agent-kourier-0 -c alertmanager -- wget -qO- http://localhost:9093/api/v2/status)
jq -e '.config.original | contains("receiver: monitoring/sink/sink") and contains("receiver: checkout-alerts")' >/dev/null <<<"${am}" ||
  fail "the running Alertmanager configuration lacks the sink route or the checkout-alerts route"
ok "the Alertmanager runs the e2e sink route and the checkout-alerts route"

# --- A trace through the collector into Tempo ---------------------------------------------------------------------------
svc=kourier-verify-$(date +%s)
"${K[@]}" -n "${NS}" run "telemetrygen-${svc#kourier-verify-}" --image="${TELEMETRYGEN_IMAGE}" --restart=Never --quiet \
  --overrides='{"spec":{"containers":[{"name":"telemetrygen","image":"'"${TELEMETRYGEN_IMAGE}"'","args":["traces","--otlp-endpoint","otel-collector.observability.svc:4317","--otlp-insecure","--traces","1","--service","'"${svc}"'"],"resources":{"limits":{"cpu":"200m","memory":"64Mi"}}}]}}' >/dev/null
"${K[@]}" -n "${NS}" wait --for=jsonpath='{.status.phase}'=Succeeded "pod/telemetrygen-${svc#kourier-verify-}" --timeout=180s >/dev/null ||
  fail "telemetrygen did not finish"
"${K[@]}" -n "${NS}" delete pod "telemetrygen-${svc#kourier-verify-}" --wait=false >/dev/null
trace_id=
for _ in $(seq 1 30); do
  trace_id=$(prom_get "http://tempo.observability.svc:3200/api/search?tags=service.name%3D${svc}&limit=5" | jq -r '.traces[0].traceID // empty')
  [ -z "${trace_id}" ] || break
  sleep 3
done
[ -n "${trace_id}" ] || fail "no trace of service ${svc} in Tempo"
trace=$(prom_get "http://tempo.observability.svc:3200/api/v2/traces/${trace_id}")
spans=$(jq '[.. | objects | select(has("spanId"))] | length' <<<"${trace}")
pod=$(jq -r '[.. | objects | select(.key? == "k8s.pod.name") | .value.stringValue] | first // empty' <<<"${trace}")
ok "trace ${trace_id} of service ${svc} is in Tempo (${spans} spans; k8s.pod.name=${pod:-<none: k8sattributes did not match>})"

# --- Grafana ------------------------------------------------------------------------------------------------------------
# shellcheck disable=SC2016 # expanded by the shell in the Grafana pod
dash=$("${K[@]}" -n "${NS}" exec deploy/grafana -c grafana -- sh -c \
  'wget -qO- --header "Authorization: Basic $(printf %s:%s "$GF_SECURITY_ADMIN_USER" "$GF_SECURITY_ADMIN_PASSWORD" | base64 | tr -d "\n")" http://localhost:3000/api/dashboards/uid/commerce')
jq -e '.dashboard.title == "Commerce"' >/dev/null <<<"${dash}" || fail "the Commerce dashboard is not provisioned"
ok "Grafana dashboard Commerce (folder $(jq -r .meta.folderTitle <<<"${dash}"), $(jq '[.dashboard.panels[] | select(.type != "row")] | length' <<<"${dash}") panels)"
# shellcheck disable=SC2016
ds=$("${K[@]}" -n "${NS}" exec deploy/grafana -c grafana -- sh -c \
  'wget -qO- --header "Authorization: Basic $(printf %s:%s "$GF_SECURITY_ADMIN_USER" "$GF_SECURITY_ADMIN_PASSWORD" | base64 | tr -d "\n")" http://localhost:3000/api/datasources')
ok "Grafana datasources: $(jq -r 'map("\(.name) (\(.uid))") | join(", ")' <<<"${ds}")"
"${K[@]}" -n demo-tools get secret demo-grafana-token -o json | jq -e '.data.token | length > 0' >/dev/null ||
  fail "demo-tools/demo-grafana-token has no token"
# The token goes to the Grafana pod on stdin: it is on no argument list on this machine and never on the terminal.
# shellcheck disable=SC2016
"${K[@]}" -n demo-tools get secret demo-grafana-token -o json | jq -r .data.token | base64 -d |
  "${K[@]}" -n "${NS}" exec -i deploy/grafana -c grafana -- sh -c \
    'read -r t; wget -qO- --header "Authorization: Bearer $t" "http://localhost:3000/api/datasources/proxy/uid/prometheus/api/v1/query?query=up" >/dev/null' ||
  fail "the token in demo-tools/demo-grafana-token cannot query Prometheus through Grafana"
ok "the token in demo-tools/demo-grafana-token queries Prometheus through Grafana"

# --- Memory -------------------------------------------------------------------------------------------------------------
echo "memory: kind node $(docker stats --no-stream --format '{{.MemUsage}}' "${NODE}")"
echo "memory: working sets in ${NS} (cAdvisor, from the kubelet):"
"${K[@]}" get --raw "/api/v1/nodes/${NODE}/proxy/metrics/cadvisor" |
  awk -v ns="${NS}" '/^container_memory_working_set_bytes\{/ && $0 ~ "namespace=\"" ns "\"" && $0 !~ /container=""/ && $0 ~ /container="/ {
      match($0, /container="[^"]*"/); c = substr($0, RSTART + 11, RLENGTH - 12)
      match($0, /pod="[^"]*"/); p = substr($0, RSTART + 5, RLENGTH - 6)
      v = $(NF - 1); if (v !~ /^[0-9.e+]+$/) v = $NF
      printf "   %-48s %-36s %6.1f MiB\n", p, c, v / 1048576; total += v
    } END { printf "   %-85s %6.1f MiB\n", "total", total / 1048576 }'
