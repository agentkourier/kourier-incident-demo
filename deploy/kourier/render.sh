#!/usr/bin/env bash
# Render the three releases' config directories into rendered/<name>/, filling in the release name and the
# workspace IDs from ids.env. Usage: deploy/kourier/render.sh
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
ids=${DEMO_IDS:-$here/ids.env}
[ -f "$ids" ] || { echo "missing $ids (copy ids.example.env and fill it in)" >&2; exit 1; }
# shellcheck disable=SC1090
. "$ids"
for name in checkout ic payments; do
  out=$here/rendered/$name; rm -rf "$out"; mkdir -p "$out"
  for f in "$here/$name"/*.yaml; do
    sed -e "s/__RELEASE__/agent-kourier-demo-$name/g" -e "s/__CHECKOUT_CHANNEL__/$CHECKOUT_CHANNEL/g" \
        -e "s/__INCIDENTS_CHANNEL__/$INCIDENTS_CHANNEL/g" -e "s/__PAYMENTS_CHANNEL__/$PAYMENTS_CHANNEL/g" \
        -e "s/__ALERT_BOT__/$ALERT_BOT/g" -e "s/__PAGER_BOT__/$PAGER_BOT/g" \
        -e "s/__PAYMENTS_APPROVERS__/$PAYMENTS_APPROVERS/g" "$f" > "$out/$(basename "$f")"
  done
  if grep -q '__[A-Z_]*__' "$out"/*.yaml; then echo "unfilled placeholder in $out" >&2; exit 1; fi
done
echo "rendered: $here/rendered/{checkout,ic,payments}"
