#!/usr/bin/env bash
# Put the demo back to its starting state between takes: payments recreated on v2.13.0 with a one-revision history,
# checkout and loadgen running, and no CheckoutHighErrorRate alert. Slack threads are not touched.
set -euo pipefail
here=$(cd "$(dirname "$0")/.." && pwd)
ctx=${KUBE_CONTEXT:-kind-agent-kourier-full}
K=(kubectl --context "$ctx")
"${K[@]}" -n commerce delete deployment payments --ignore-not-found --wait=true >/dev/null
"${K[@]}" apply -k "$here/deploy/commerce" >/dev/null
"${K[@]}" apply -f "$here/loadgen/deploy/loadgen.yaml" >/dev/null
"${K[@]}" -n commerce set env deploy/loadgen RATE="${LOADGEN_RATE:-3}" >/dev/null
"${K[@]}" -n commerce rollout status deploy/payments --timeout=300s >/dev/null
"${K[@]}" -n commerce rollout status deploy/checkout --timeout=120s >/dev/null
echo "payments is on v2.13.0 (revision 1). CheckoutHighErrorRate clears about 2 minutes after errors stop;"
echo "wait for it before the next take: kubectl --context $ctx -n observability port-forward svc/prometheus-server 9090:80"
