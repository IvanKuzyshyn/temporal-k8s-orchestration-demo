#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/kind-common.sh"
assert_context
case "${1:-}" in
  api) namespace=temporal-demo-orchestrator; service=orchestrator; port=${API_FORWARD_PORT:-18080} ;;
  ui) namespace=temporal; service=temporal-ui; port=${UI_FORWARD_PORT:-18088} ;;
  *) echo 'Usage: kind-forward.sh api|ui' >&2; exit 2 ;;
esac
[[ $port =~ ^[0-9]+$ && ${#port} -le 5 ]] && ((10#$port>0 && 10#$port<=65535)) || { echo 'Forward port must be1..65535' >&2; exit 1; }
# Foreground process: Ctrl-C cleans up only this tunnel. No global context changes.
exec kubectl --context "$CONTEXT" --request-timeout=15s -n "$namespace" port-forward --address 127.0.0.1 "service/$service" "$port:8080"
