#!/usr/bin/env bash
# Remove the demo's three Agent Kourier releases and give the payments Slack app back to the tier's sandbox release.
set -euo pipefail
ctx=${KUBE_CONTEXT:-kind-agent-kourier-full}
K=(kubectl --context "$ctx" -n agent-kourier)
for name in checkout ic payments; do
  rel=agent-kourier-demo-$name
  helm --kube-context "$ctx" uninstall "$rel" -n agent-kourier --ignore-not-found >/dev/null
  "${K[@]}" delete secret "$rel-slack" "$rel-dex-client" "$rel-token" --ignore-not-found >/dev/null
  "${K[@]}" delete configmap "$rel-config" --ignore-not-found >/dev/null
  "${K[@]}" delete job "$rel-token-bringup" --ignore-not-found >/dev/null
done
if "${K[@]}" get deploy/agent-kourier-sandbox >/dev/null 2>&1; then
  "${K[@]}" scale deploy/agent-kourier-sandbox --replicas=1 >/dev/null
  echo "agent-kourier-sandbox scaled back to 1"
fi
