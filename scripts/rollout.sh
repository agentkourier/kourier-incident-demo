#!/usr/bin/env bash
# Deploy a payments version the way a CD system would: image, version label and git revision in one change.
# Usage: scripts/rollout.sh v2.14.0   (or v2.13.0). KUBE_CONTEXT defaults to kind-agent-kourier-full.
set -euo pipefail
version=${1:?usage: rollout.sh <payments version, e.g. v2.14.0>}
ctx=${KUBE_CONTEXT:-kind-agent-kourier-full}
case "$version" in
  v2.13.0) rev=62a0f589878145167725eb2656f3952aca6ff559 ;;
  v2.14.0) rev=85d062e352111028b0a3fa5204faa18be63b58df ;;
  *) echo "unknown payments version $version (v2.13.0 or v2.14.0)" >&2; exit 2 ;;
esac
patch=$(cat <<JSON
{"spec":{"template":{"metadata":{"labels":{"app.kubernetes.io/version":"$version"},
 "annotations":{"demo.agentkourier.dev/git-revision":"$rev","kubernetes.io/change-cause":"deploy payments $version"}},
 "spec":{"containers":[{"name":"payments","image":"demo-payments:$version"}]}}}}
JSON
)
kubectl --context "$ctx" -n commerce patch deployment payments --type=strategic -p "$patch"
echo "payments rolling to $version (git $rev); watch: kubectl --context $ctx -n commerce rollout status deploy/payments"
