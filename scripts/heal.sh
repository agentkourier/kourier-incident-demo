#!/usr/bin/env bash
# After Docker Desktop or the kind node restarts, Agent Substrate keeps the kagent worker's old pod IP, logs
# "registered worker IP disagrees with its pod", and every agent turn fails with "input was not accepted; retry after
# the session becomes available". Restarting the worker makes it register again.
# After a long sleep, Substrate's router can also hold an expired pod certificate: its log says "substrate ResumeActor
# failed ... CertificateExpired", and every turn is refused as not ready (kagent -32004). Restarting the router and the
# egress proxy gives them fresh certificates.
set -euo pipefail
ctx=${KUBE_CONTEXT:-kind-agent-kourier-full}
kubectl --context "$ctx" -n kagent rollout restart deploy/kagent-default >/dev/null
kubectl --context "$ctx" -n kagent rollout status deploy/kagent-default --timeout=180s
kubectl --context "$ctx" -n ate-system rollout restart deploy/atenet-router deploy/atenet-egress >/dev/null
kubectl --context "$ctx" -n ate-system rollout status deploy/atenet-router --timeout=180s
kubectl --context "$ctx" -n ate-system rollout status deploy/atenet-egress --timeout=180s
