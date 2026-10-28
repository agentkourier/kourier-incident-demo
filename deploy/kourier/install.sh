#!/usr/bin/env bash
# Install or update the demo's three Agent Kourier releases in namespace agent-kourier on the kind full tier:
#   agent-kourier-demo-checkout  checkout-assist     (Slack app tokens: ~/.config/agent-kourier/demo-checkout-assist.env)
#   agent-kourier-demo-ic        incident-commander  (~/.config/agent-kourier/demo-incident-commander.env)
#   agent-kourier-demo-payments  payments-assist     (~/.config/agent-kourier/demo-payments-assist.env)
# Needs AGENT_KOURIER_CHECKOUT (an agent-kourier checkout, for the chart). Never prints a token.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
ctx=${KUBE_CONTEXT:-kind-agent-kourier-full}
ak=${AGENT_KOURIER_CHECKOUT:?set AGENT_KOURIER_CHECKOUT to an agent-kourier checkout (for charts/agent-kourier)}
cfg=${DEMO_SECRETS_DIR:-$HOME/.config/agent-kourier}
K=(kubectl --context "$ctx" -n agent-kourier)
"$here/render.sh"

# secret NAME KEY=ENVFILE:VAR...  builds a Secret from values in env files, through stdin only.
secret() {
  local name=$1; shift
  python3 - "$name" "$@" <<'PY' | "${K[@]}" apply -f - >/dev/null
import sys,os,json,base64
name=sys.argv[1]; data={}
for spec in sys.argv[2:]:
    key,src=spec.split('=',1)
    if src.startswith('literal:'): val=src[len('literal:'):]
    else:
        f,var=src.rsplit(':',1); kv={}
        for l in open(os.path.expanduser(f)):
            l=l.strip()
            if l and not l.startswith('#') and '=' in l: k,v=l.split('=',1); kv[k]=v.strip().strip('"')
        val=kv[var]
    data[key]=base64.b64encode(val.encode()).decode()
print(json.dumps({"apiVersion":"v1","kind":"Secret","metadata":{"name":name,"namespace":"agent-kourier"},"type":"Opaque","data":data}))
PY
}

# Per release: its Slack app's token file, and one of the full tier's Dex test users (hack/kind/full/dex.yaml), so
# that kagent keeps each release's sessions apart.
app_file() { case $1 in checkout) echo demo-checkout-assist.env ;; ic) echo demo-incident-commander.env ;; payments) echo demo-payments-assist.env ;; esac; }
dex_user() { case $1 in checkout) echo binding-test@example.test ;; ic) echo identity-b@example.test ;; payments) echo svc-sre-alerts@example.test ;; esac; }
for name in checkout ic payments; do
  rel=agent-kourier-demo-$name
  secret "$rel-slack" "botToken=$cfg/$(app_file $name):SLACK_BOT_TOKEN" "appToken=$cfg/$(app_file $name):SLACK_APP_TOKEN"
  # The full tier's fake Dex client and test password (fixture values from hack/kind/full/dex.yaml, not secrets).
  secret "$rel-dex-client" "clientSecret=literal:fake-agent-kourier-binding-secret" "username=literal:$(dex_user $name)" "password=literal:password"
  # The refresh CronJob patches the token Secret and cannot create it; the bring-up Job below fills it.
  "${K[@]}" get secret "$rel-token" >/dev/null 2>&1 || secret "$rel-token" "token=literal:pending"
  "${K[@]}" create configmap "$rel-config" --from-file="$here/rendered/$name" --dry-run=client -o yaml | "${K[@]}" apply -f - >/dev/null
  helm --kube-context "$ctx" upgrade --install "$rel" "$ak/charts/agent-kourier" -n agent-kourier -f "$here/values.yaml" \
    --set config.existingConfigMap="$rel-config" --set tokenRefresh.targetSecret="$rel-token" \
    --set tokenRefresh.dex.credentialsSecret="$rel-dex-client" --wait --timeout 4m >/dev/null
  "${K[@]}" delete job "$rel-token-bringup" --ignore-not-found >/dev/null
  "${K[@]}" create job "$rel-token-bringup" --from="cronjob/$rel-token-refresh" >/dev/null
  "${K[@]}" wait --for=condition=complete "job/$rel-token-bringup" --timeout=120s >/dev/null
  "${K[@]}" rollout restart "deploy/$rel" >/dev/null
  "${K[@]}" rollout status "deploy/$rel" --timeout=180s >/dev/null
  echo "ok: $rel"
done
