#!/usr/bin/env bash
# Offline checks of what install.sh installs, needing only Docker, helm, yq (and the Helm repositories install.sh adds):
#   - the Prometheus chart renders; promtool checks the rule files it renders and runs tests/rules.test.yaml against them
#   - amtool checks alertmanager/config.yaml, with the webhook and without (the two forms install.sh applies), and tests its routes
# With --cluster, also the configuration the operator generated on the cluster (KUBE_CONTEXT, default kind-agent-kourier-full):
# amtool checks it, and its routes send CheckoutHighErrorRate to both the e2e sink and checkout-alerts.
# No real webhook URL is needed: a fake one is mounted where api_url_file points.
set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
CTX=${KUBE_CONTEXT:-kind-agent-kourier-full}
PROMETHEUS_CHART_VERSION=29.39.0
PROMETHEUS_IMAGE=quay.io/prometheus/prometheus:v3.15.1 # the chart's appVersion
ALERTMANAGER_IMAGE=quay.io/prometheus/alertmanager:v0.34.0 # what prometheus-operator runs on the cluster
CLUSTER=0
[ "${1:-}" != --cluster ] || CLUSTER=1

TMP=$(mktemp -d)
trap 'rm -rf "${TMP}"' EXIT
fail() {
  echo "FAIL: $*" >&2
  exit 1
}
ok() { echo "ok: $*"; }

# --- Prometheus rules ---------------------------------------------------------------------------------------------------
mkdir -p "${TMP}/prom"
helm template prometheus prometheus-community/prometheus --version "${PROMETHEUS_CHART_VERSION}" --namespace observability \
  -f "${HERE}/values/prometheus.yaml" -f "${HERE}/values/prometheus-rules.yaml" >"${TMP}/prom/rendered.yaml"
for f in recording_rules.yml alerting_rules.yml prometheus.yml; do
  yq "select(.kind == \"ConfigMap\" and .metadata.name == \"prometheus-server\") | .data[\"${f}\"]" "${TMP}/prom/rendered.yaml" >"${TMP}/prom/${f}"
done
cp "${HERE}/tests/rules.test.yaml" "${TMP}/prom/"
promtool() { docker run --rm -v "${TMP}/prom:/w" -w /w --entrypoint /bin/promtool "${PROMETHEUS_IMAGE}" "$@"; }
promtool check rules recording_rules.yml alerting_rules.yml >/dev/null || fail "promtool check rules"
ok "promtool check rules: the rendered rule files"
promtool test rules rules.test.yaml >/dev/null || { promtool test rules rules.test.yaml; fail "promtool test rules"; }
ok "promtool test rules: CheckoutHighErrorRate fires at 9.1%, not at 1.6%; the per-version 5xx ratio"

# --- Alertmanager -------------------------------------------------------------------------------------------------------
mkdir -p "${TMP}/am/secret"
printf 'https://hooks.slack.com/services/T00000000/B00000000/fakefakefakefakefake\n' >"${TMP}/am/secret/webhook-url"
cp "${HERE}/alertmanager/config.yaml" "${TMP}/am/with-webhook.yaml"
yq 'del(.receivers[] | select(.name == "checkout-alerts") | .slack_configs)' "${HERE}/alertmanager/config.yaml" >"${TMP}/am/without-webhook.yaml"
amtool() {
  docker run --rm -v "${TMP}/am:/w:ro" -v "${TMP}/am/secret:/etc/alertmanager/secrets/demo-checkout-alerts-slack:ro" \
    -w /w --entrypoint /bin/amtool "${ALERTMANAGER_IMAGE}" "$@"
}
# route_test CONFIG EXPECTED LABEL=VALUE...: the alert with these labels goes to the receivers EXPECTED (comma-separated, in order).
route_test() {
  local config=$1 want=$2 got
  shift 2
  got=$(amtool config routes test --config.file="${config}" "$@")
  [ "${got}" = "${want}" ] || fail "${config}: $* routed to '${got}', want '${want}'"
  ok "${config}: $* -> ${want}"
}
for config in with-webhook.yaml without-webhook.yaml; do
  amtool check-config "${config}" >/dev/null || { amtool check-config "${config}"; fail "amtool check-config ${config}"; }
  ok "amtool check-config ${config}"
  route_test "${config}" checkout-alerts alertname=CheckoutHighErrorRate severity=critical team=checkout service=checkout namespace=commerce
  route_test "${config}" checkout-alerts alertname=CheckoutLatencyHigh severity=critical team=checkout
  route_test "${config}" null alertname=Something severity=critical team=payments
done
grep -q 'title: .{{ .Status | toUpper }}{{ if eq .Status "firing" }} ({{ .Alerts.Firing | len }}){{ end }} - {{ .CommonLabels.alertname }}.$' \
  "${TMP}/am/with-webhook.yaml" || fail "the Slack title is not 'FIRING (<n>) - <alertname>' / 'RESOLVED - <alertname>'"
ok "the Slack title template has no prefix"

# --- The operator's configuration on the cluster ------------------------------------------------------------------------
if [ "${CLUSTER}" = 1 ]; then
  kubectl --context "${CTX}" -n monitoring get secret alertmanager-agent-kourier-generated -o json |
    jq -r '.data["alertmanager.yaml.gz"]' | base64 -d | gunzip >"${TMP}/am/generated.yaml"
  amtool check-config generated.yaml >/dev/null || fail "amtool check-config on the operator's generated configuration"
  ok "amtool check-config: the operator's generated configuration"
  route_test generated.yaml monitoring/sink/sink,checkout-alerts alertname=CheckoutHighErrorRate severity=critical team=checkout service=checkout namespace=commerce
  route_test generated.yaml monitoring/sink/sink alertname=E2EAlert severity=warning
fi
