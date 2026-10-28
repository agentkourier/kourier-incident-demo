#!/usr/bin/env bash
# After Docker Desktop or the kind node restarts, Agent Substrate keeps the kagent worker's old pod IP, logs
# "registered worker IP disagrees with its pod", and every agent turn fails with "input was not accepted; retry after
# the session becomes available". Restarting the worker makes it register again.
set -euo pipefail
ctx=${KUBE_CONTEXT:-kind-agent-kourier-full}
kubectl --context "$ctx" -n kagent rollout restart deploy/kagent-default >/dev/null
kubectl --context "$ctx" -n kagent rollout status deploy/kagent-default --timeout=180s
