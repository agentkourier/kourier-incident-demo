#!/usr/bin/env bash
# Install the demo's observability stack on the kind cluster and point its Alertmanager at #checkout-alerts. Safe to re-run.
#
#   namespace observability: Prometheus (+ kube-state-metrics), Tempo, the OpenTelemetry collector, Grafana (pinned Helm charts,
#                            values in values/), the Commerce dashboard, the Grafana admin Secret (generated once, never printed)
#   namespace demo-tools:    Secret demo-grafana-token (a Grafana service account token for an MCP server; never printed)
#   namespace commerce:      created, empty (the demo services and the load generator go there)
#   namespace monitoring:    the Alertmanager agent-kourier gets a base configuration (alertmanager/config.yaml) and, when the
#                            webhook file is there, the Secret it reads the webhook URL from. Its state before is saved in the
#                            Secret monitoring/demo-alertmanager-previous; alertmanager/restore.sh puts it back.
#
# Environment: KUBE_CONTEXT (default kind-agent-kourier-full), CHECKOUT_ALERTS_WEBHOOK_FILE (default
# ~/.config/agent-kourier/demo-checkout-alerts-webhook-url), ALERTMANAGER_EXTERNAL_URL (default http://localhost:9093: the
# address in the alert's title link; `kubectl -n monitoring port-forward svc/alertmanager 9093` makes it work).
# Needs kubectl, helm, jq, yq, curl, openssl.
set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
CTX=${KUBE_CONTEXT:-kind-agent-kourier-full}
NS=observability
WEBHOOK_FILE=${CHECKOUT_ALERTS_WEBHOOK_FILE:-${HOME}/.config/agent-kourier/demo-checkout-alerts-webhook-url}
AM_EXTERNAL_URL=${ALERTMANAGER_EXTERNAL_URL:-http://localhost:9093}
K=(kubectl --context "${CTX}")
H=(helm --kube-context "${CTX}")

PROMETHEUS_CHART_VERSION=29.39.0
TEMPO_CHART_VERSION=1.24.4
OTELCOL_CHART_VERSION=0.178.0
GRAFANA_CHART_VERSION=10.5.15

AM_NAME=agent-kourier # the operator-managed Alertmanager in monitoring
AM_CONFIG_SECRET=alertmanager-demo-config
AM_WEBHOOK_SECRET=demo-checkout-alerts-slack
AM_BACKUP_SECRET=demo-alertmanager-previous
FIELD_MANAGER=kourier-incident-demo
GRAFANA_SA=kourier-mcp

MISSING=()
PF_PID=
TMP=$(mktemp -d)
cleanup() {
  [ -z "${PF_PID}" ] || kill "${PF_PID}" 2>/dev/null || true
  rm -rf "${TMP}"
}
trap cleanup EXIT

step() { echo "==> $*"; }
die() {
  echo "error: $*" >&2
  exit 1
}

# apply_secret NAMESPACE NAME KEY=VARIABLE...: a Secret whose KEY holds what the shell variable VARIABLE holds, by a server-side
# apply on stdin. printf is a builtin, so no value is on an argument list or in the environment of a child.
apply_secret() {
  local ns=$1 name=$2 pair var
  shift 2
  {
    printf 'apiVersion: v1\nkind: Secret\nmetadata:\n  name: %s\n  namespace: %s\ntype: Opaque\ndata:\n' "${name}" "${ns}"
    for pair in "$@"; do
      var=${pair#*=}
      printf '  %s: %s\n' "${pair%%=*}" "$(printf %s "${!var}" | base64 | tr -d '\n')"
    done
  } | "${K[@]}" apply --server-side --force-conflicts --field-manager="${FIELD_MANAGER}" -f - >/dev/null
}

# secret_value NAMESPACE NAME KEY: the decoded value, on stdout (for a variable, never for the terminal).
secret_value() {
  "${K[@]}" -n "$1" get secret "$2" -o json | jq -r --arg k "$3" '.data[$k] // empty' | base64 -d
}

# webhook_url_load FILE: set WEBHOOK_URL from FILE, or exit naming what is wrong (never what is in it). The checks are the
# agent-kourier sandbox's: a regular file of yours, mode 600 or stricter, one line holding an incoming-webhook URL.
webhook_url_load() {
  local file=$1 line value="" mode LC_ALL=C re='^https://hooks\.slack\.com/services/T[A-Z0-9]+/B[A-Z0-9]+/[A-Za-z0-9]+$'
  [ ! -L "${file}" ] || die "${file}: is a symbolic link; point CHECKOUT_ALERTS_WEBHOOK_FILE at the file itself"
  [ -f "${file}" ] || die "${file}: is not a regular file"
  [ -O "${file}" ] || die "${file}: is not owned by you"
  mode=$(stat -f '%Lp' "${file}" 2>/dev/null || stat -c '%a' "${file}")
  [ $((8#${mode} & 8#077)) -eq 0 ] || die "${file}: has mode ${mode}; it holds a secret, so chmod 600 it"
  while IFS= read -r line || [ -n "${line}" ]; do
    line=${line%$'\r'}
    line=${line#"${line%%[![:space:]]*}"}
    line=${line%"${line##*[![:space:]]}"}
    [ -n "${line}" ] || continue
    [ -z "${value}" ] || die "${file}: has more than one line; it holds the URL and nothing else"
    value=${line}
  done <"${file}"
  [ -n "${value}" ] || die "${file}: is empty"
  [[ ${value} =~ ${re} ]] || die "${file}: does not hold a Slack incoming-webhook URL (https://hooks.slack.com/services/T.../B.../...)"
  WEBHOOK_URL=${value}
}

# ---------------------------------------------------------------------------------------------------------------------------
step "preflight (context ${CTX})"
for tool in kubectl helm jq yq curl openssl; do
  command -v "${tool}" >/dev/null || die "${tool} is required"
done
"${K[@]}" get --raw /readyz >/dev/null || die "the cluster of context ${CTX} is not reachable"
"${K[@]}" -n monitoring get alertmanager "${AM_NAME}" >/dev/null || die "no Alertmanager monitoring/${AM_NAME} (prometheus-operator's) in ${CTX}"

WEBHOOK_URL=
if [ -e "${WEBHOOK_FILE}" ] || [ -L "${WEBHOOK_FILE}" ]; then
  webhook_url_load "${WEBHOOK_FILE}"
  echo "ok: ${WEBHOOK_FILE} holds an incoming-webhook URL (not printed)"
else
  MISSING+=("${WEBHOOK_FILE}: the #checkout-alerts incoming-webhook URL, on one line, mode 600. Without it the Alertmanager routes CheckoutHighErrorRate to checkout-alerts but posts nothing; add the file and re-run ${HERE}/install.sh")
  echo "missing: ${WEBHOOK_FILE} (the Slack receiver is installed without a webhook; see the summary)"
fi

step "namespaces"
for ns in "${NS}" demo-tools commerce; do
  "${K[@]}" create namespace "${ns}" --dry-run=client -o yaml | "${K[@]}" apply -f - >/dev/null
done

step "Helm repositories"
add_repo() {
  local name=$1 url=$2 have
  have=$(helm repo list -o json 2>/dev/null | jq -r --arg n "${name}" '.[] | select(.name == $n) | .url')
  if [ -z "${have}" ]; then
    helm repo add "${name}" "${url}" >/dev/null
  elif [ "${have%/}" != "${url%/}" ]; then
    die "Helm repository ${name} points at ${have}, not ${url}"
  fi
}
add_repo prometheus-community https://prometheus-community.github.io/helm-charts
add_repo grafana https://grafana.github.io/helm-charts
add_repo open-telemetry https://open-telemetry.github.io/opentelemetry-helm-charts
helm repo update prometheus-community grafana open-telemetry >/dev/null # only these: other repos on a machine may be broken

step "Grafana admin Secret and the Commerce dashboard"
if ! "${K[@]}" -n "${NS}" get secret grafana-admin >/dev/null 2>&1; then
  # shellcheck disable=SC2034 # read by apply_secret, by name
  GRAFANA_ADMIN_USER="admin"
  # shellcheck disable=SC2034
  GRAFANA_ADMIN_PASSWORD=$(openssl rand -base64 33 | tr -d '/+=\n' | cut -c1-32)
  apply_secret "${NS}" grafana-admin admin-user=GRAFANA_ADMIN_USER admin-password=GRAFANA_ADMIN_PASSWORD
  echo "generated Secret ${NS}/grafana-admin"
fi
"${K[@]}" -n "${NS}" create configmap grafana-dashboards-commerce --from-file="${HERE}/dashboards" --dry-run=client -o yaml |
  "${K[@]}" apply --server-side --force-conflicts --field-manager="${FIELD_MANAGER}" -f - >/dev/null

step "Helm releases"
install_release() {
  local release=$1 chart=$2 version=$3
  shift 3
  echo "  ${release}: ${chart} ${version}"
  "${H[@]}" upgrade --install "${release}" "${chart}" --version "${version}" --namespace "${NS}" "$@" --wait --timeout 6m >/dev/null
}
install_release prometheus prometheus-community/prometheus "${PROMETHEUS_CHART_VERSION}" \
  -f "${HERE}/values/prometheus.yaml" -f "${HERE}/values/prometheus-rules.yaml"
install_release tempo grafana/tempo "${TEMPO_CHART_VERSION}" -f "${HERE}/values/tempo.yaml"
install_release otel-collector open-telemetry/opentelemetry-collector "${OTELCOL_CHART_VERSION}" -f "${HERE}/values/otel-collector.yaml"
install_release grafana grafana/grafana "${GRAFANA_CHART_VERSION}" -f "${HERE}/values/grafana.yaml"

# ---------------------------------------------------------------------------------------------------------------------------
step "Alertmanager monitoring/${AM_NAME}: route to checkout-alerts"
am_json=$("${K[@]}" -n monitoring get alertmanager "${AM_NAME}" -o json)
current_config_secret=$(jq -r '.spec.configSecret // ""' <<<"${am_json}")
if [ "${current_config_secret}" != "${AM_CONFIG_SECRET}" ]; then
  # The state this script replaces, kept for restore.sh: the three fields it sets, and the base configuration they named
  # (which can hold a URL, hence a Secret). Refreshed whenever someone else's configuration is in place.
  # shellcheck disable=SC2034 # read by apply_secret, by name
  PREVIOUS_SPEC=$(jq -c '{configSecret: .spec.configSecret, secrets: .spec.secrets, externalUrl: .spec.externalUrl}' <<<"${am_json}")
  # shellcheck disable=SC2034
  PREVIOUS_CONFIG=
  if [ -n "${current_config_secret}" ]; then
    PREVIOUS_CONFIG=$(secret_value monitoring "${current_config_secret}" alertmanager.yaml 2>/dev/null || true)
  fi
  if [ -n "${PREVIOUS_CONFIG}" ]; then
    apply_secret monitoring "${AM_BACKUP_SECRET}" spec.json=PREVIOUS_SPEC alertmanager.yaml=PREVIOUS_CONFIG
  else
    "${K[@]}" -n monitoring delete secret "${AM_BACKUP_SECRET}" --ignore-not-found >/dev/null # no stale alertmanager.yaml key
    apply_secret monitoring "${AM_BACKUP_SECRET}" spec.json=PREVIOUS_SPEC
  fi
  echo "saved the Alertmanager's previous state in Secret monitoring/${AM_BACKUP_SECRET}"
fi

if [ -n "${WEBHOOK_URL}" ]; then
  # shellcheck disable=SC2034 # read by apply_secret, by name
  AM_CONFIG=$(<"${HERE}/alertmanager/config.yaml")
  # The webhook Secret first, so the operator never mounts a Secret that is not there.
  apply_secret monitoring "${AM_WEBHOOK_SECRET}" webhook-url=WEBHOOK_URL
  secrets_json="[\"${AM_WEBHOOK_SECRET}\"]"
else
  # shellcheck disable=SC2034
  AM_CONFIG=$(yq 'del(.receivers[] | select(.name == "checkout-alerts") | .slack_configs)' "${HERE}/alertmanager/config.yaml")
  secrets_json=null
fi
apply_secret monitoring "${AM_CONFIG_SECRET}" alertmanager.yaml=AM_CONFIG
# Only these three fields, by a JSON merge patch; every other field of the tier's Alertmanager stays as it is. Not a
# server-side apply: the tier made this Alertmanager with a client-side `kubectl apply`, and `kubectl apply --server-side`
# moves the client-side apply's fields (the AlertmanagerConfig selectors, replicas) to the new field manager and then
# deletes them, because the applied manifest does not have them. Without the webhook, `secrets` is null (removed).
"${K[@]}" -n monitoring patch alertmanager "${AM_NAME}" --type=merge -p "$(jq -cn \
  --arg cs "${AM_CONFIG_SECRET}" --arg url "${AM_EXTERNAL_URL}" --argjson s "${secrets_json}" \
  '{spec: {configSecret: $cs, secrets: $s, externalUrl: $url}}')" >/dev/null

# The operator regenerates the configuration and rolls the pod when the mounts or the URL change. Wait until the running
# Alertmanager has the route, the e2e sink's route and the external URL.
am_ready() {
  local status
  "${K[@]}" -n monitoring get pod "alertmanager-${AM_NAME}-0" -o json | jq -e '.status.containerStatuses | all(.ready)' >/dev/null &&
    "${K[@]}" -n monitoring get pod "alertmanager-${AM_NAME}-0" -o json |
    jq -e --arg u "--web.external-url=${AM_EXTERNAL_URL}" '[.spec.containers[] | select(.name == "alertmanager") | .args[]] | index($u) != null' >/dev/null &&
    status=$("${K[@]}" -n monitoring exec "alertmanager-${AM_NAME}-0" -c alertmanager -- wget -q -O - http://localhost:9093/api/v2/status) &&
    jq -e '.config.original | contains("receiver: checkout-alerts") and contains("receiver: monitoring/sink/sink")' >/dev/null <<<"${status}"
}
deadline=$((SECONDS + 240))
until am_ready 2>/dev/null; do
  [ "${SECONDS}" -lt "${deadline}" ] || die "the Alertmanager did not load the checkout-alerts route in 240 s"
  sleep 5
done
if [ -n "${WEBHOOK_URL}" ]; then
  "${K[@]}" -n monitoring exec "alertmanager-${AM_NAME}-0" -c alertmanager -- \
    test -s "/etc/alertmanager/secrets/${AM_WEBHOOK_SECRET}/webhook-url" || die "the webhook file is not mounted in the Alertmanager"
  echo "ok: the Alertmanager posts checkout alerts to #checkout-alerts"
else
  echo "ok: the Alertmanager routes checkout alerts to checkout-alerts (no webhook yet: nothing is posted)"
fi

# ---------------------------------------------------------------------------------------------------------------------------
step "Grafana service account ${GRAFANA_SA} and Secret demo-tools/demo-grafana-token"
"${K[@]}" -n "${NS}" rollout status deploy/grafana --timeout=180s >/dev/null
"${K[@]}" -n "${NS}" port-forward svc/grafana :80 >"${TMP}/pf.log" 2>&1 &
PF_PID=$!
GRAFANA=
for _ in $(seq 1 30); do
  port=$(sed -n 's/^Forwarding from 127\.0\.0\.1:\([0-9]*\) .*/\1/p' "${TMP}/pf.log" | head -1)
  if [ -n "${port}" ] && curl -fsS "http://127.0.0.1:${port}/api/health" >/dev/null 2>&1; then
    GRAFANA=http://127.0.0.1:${port}
    break
  fi
  sleep 1
done
[ -n "${GRAFANA}" ] || die "could not reach Grafana through a port-forward"

GRAFANA_ADMIN_AUTH="$(secret_value "${NS}" grafana-admin admin-user):$(secret_value "${NS}" grafana-admin admin-password)"
# grafana_admin METHOD PATH [BODY]: the response body. The credentials go to curl on stdin (-K -), never on an argument list.
grafana_admin() {
  local args=(-fsS -K - -X "$1" "${GRAFANA}$2" -H 'Content-Type: application/json')
  [ $# -lt 3 ] || args+=(--data "$3")
  printf 'user = "%s"\n' "${GRAFANA_ADMIN_AUTH}" | curl "${args[@]}"
}
# grafana_token_works TOKEN-VARIABLE: the token can read the Commerce dashboard and query Prometheus through Grafana.
grafana_token_works() {
  local var=$1
  printf 'header = "Authorization: Bearer %s"\n' "${!var}" |
    curl -fsS -K - -o /dev/null "${GRAFANA}/api/dashboards/uid/commerce" &&
    printf 'header = "Authorization: Bearer %s"\n' "${!var}" |
    curl -fsS -K - -o /dev/null "${GRAFANA}/api/datasources/proxy/uid/prometheus/api/v1/query?query=up"
}

sa_id=$(grafana_admin GET "/api/serviceaccounts/search?query=${GRAFANA_SA}" | jq -r --arg n "${GRAFANA_SA}" '.serviceAccounts[] | select(.name == $n) | .id' | head -1)
if [ -z "${sa_id}" ]; then
  sa_id=$(grafana_admin POST /api/serviceaccounts "{\"name\":\"${GRAFANA_SA}\",\"role\":\"Viewer\",\"isDisabled\":false}" | jq -r .id)
  echo "created service account ${GRAFANA_SA} (Viewer)"
fi
EXISTING_TOKEN=
if "${K[@]}" -n demo-tools get secret demo-grafana-token >/dev/null 2>&1; then
  EXISTING_TOKEN=$(secret_value demo-tools demo-grafana-token token)
fi
if [ -n "${EXISTING_TOKEN}" ] && grafana_token_works EXISTING_TOKEN; then
  echo "ok: demo-tools/demo-grafana-token holds a working token"
else
  # Grafana has no volume, so a restarted Grafana has lost the account's tokens: replace whatever is left with a new one.
  for tid in $(grafana_admin GET "/api/serviceaccounts/${sa_id}/tokens" | jq -r '.[].id'); do
    grafana_admin DELETE "/api/serviceaccounts/${sa_id}/tokens/${tid}" >/dev/null
  done
  # shellcheck disable=SC2034 # read by apply_secret and grafana_token_works, by name
  GRAFANA_TOKEN=$(grafana_admin POST "/api/serviceaccounts/${sa_id}/tokens" "{\"name\":\"${GRAFANA_SA}-$(date +%s)\"}" | jq -r .key)
  [ -n "${GRAFANA_TOKEN}" ] && [ "${GRAFANA_TOKEN}" != null ] || die "Grafana did not return a token"
  # shellcheck disable=SC2034
  GRAFANA_URL=http://grafana.${NS}.svc
  apply_secret demo-tools demo-grafana-token token=GRAFANA_TOKEN url=GRAFANA_URL
  grafana_token_works GRAFANA_TOKEN || die "the new token cannot read the Commerce dashboard or query Prometheus"
  echo "ok: wrote a new token to demo-tools/demo-grafana-token (not printed)"
fi

# ---------------------------------------------------------------------------------------------------------------------------
step "pods in ${NS}"
"${K[@]}" -n "${NS}" wait --for=condition=Ready pod --all --timeout=180s >/dev/null
"${K[@]}" -n "${NS}" get pods

echo
echo "Installed. OTLP: otel-collector.${NS}.svc:4317 (gRPC) / :4318 (HTTP). Grafana: kubectl --context ${CTX} -n ${NS} port-forward svc/grafana 3000:80"
if [ "${#MISSING[@]}" -gt 0 ]; then
  echo
  echo "Missing:"
  for m in "${MISSING[@]}"; do echo "  - ${m}"; done
fi
