#!/usr/bin/env bash
# Put the Alertmanager monitoring/agent-kourier back as it was before install.sh: its configSecret, secrets and externalUrl
# from the Secret monitoring/demo-alertmanager-previous (and the base configuration that configSecret named, if it is gone),
# then delete the demo's two Alertmanager Secrets. The operator regenerates the configuration and rolls the pod.
# Before this demo, on kind-agent-kourier-full, all three fields were unset (previous-state.json); with no backup Secret this
# script unsets them, which is that state. Leaves the observability stack alone. Safe to re-run.
set -euo pipefail

CTX=${KUBE_CONTEXT:-kind-agent-kourier-full}
K=(kubectl --context "${CTX}")
AM_NAME=agent-kourier

spec='{"configSecret":null,"secrets":null,"externalUrl":null}'
if "${K[@]}" -n monitoring get secret demo-alertmanager-previous >/dev/null 2>&1; then
  backup=$("${K[@]}" -n monitoring get secret demo-alertmanager-previous -o json)
  spec=$(jq -r '.data["spec.json"]' <<<"${backup}" | base64 -d)
  name=$(jq -r '.configSecret // ""' <<<"${spec}")
  if [ -n "${name}" ] && ! "${K[@]}" -n monitoring get secret "${name}" >/dev/null 2>&1 &&
    jq -e '.data["alertmanager.yaml"]' >/dev/null <<<"${backup}"; then
    # The previous base configuration's Secret is gone: make it again from the copy, without the value passing an argument list.
    jq --arg n "${name}" '{apiVersion: "v1", kind: "Secret", type: "Opaque", metadata: {name: $n, namespace: "monitoring"},
      data: {"alertmanager.yaml": .data["alertmanager.yaml"]}}' <<<"${backup}" | "${K[@]}" apply -f - >/dev/null
  fi
fi
"${K[@]}" -n monitoring patch alertmanager "${AM_NAME}" --type=merge -p "$(jq -c '{spec: .}' <<<"${spec}")" >/dev/null
echo "restored spec.configSecret, spec.secrets and spec.externalUrl of monitoring/${AM_NAME}: ${spec}"

# Wait for the operator to roll the pod off the demo's Secrets before deleting them.
deadline=$((SECONDS + 180))
until ! "${K[@]}" -n monitoring get pod "alertmanager-${AM_NAME}-0" -o json |
  jq -e '[.spec.volumes[]?.secret.secretName] | index("alertmanager-demo-config") or index("demo-checkout-alerts-slack")' >/dev/null &&
  "${K[@]}" -n monitoring exec "alertmanager-${AM_NAME}-0" -c alertmanager -- wget -q -O - http://localhost:9093/api/v2/status 2>/dev/null |
  jq -e '.config.original | contains("checkout-alerts") | not' >/dev/null; do
  [ "${SECONDS}" -lt "${deadline}" ] || { echo "error: the Alertmanager did not drop the demo configuration in 180 s" >&2; exit 1; }
  sleep 3
done
"${K[@]}" -n monitoring delete secret alertmanager-demo-config demo-checkout-alerts-slack demo-alertmanager-previous --ignore-not-found >/dev/null
echo "ok: the Alertmanager runs its previous configuration; the demo's Secrets are deleted"
