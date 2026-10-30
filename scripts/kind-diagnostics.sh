#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/kind-common.sh"
assert_context
echo "Scoped diagnostics: context=$CONTEXT (no Secrets or infrastructure logs)" >&2
k get pods,pvc -A >&2 || true
k get events -A --field-selector type=Warning >&2 || true
for ns in temporal-demo-orchestrator temporal-demo-worker-a temporal-demo-worker-b temporal-demo-worker-c temporal-demo-worker-d; do
  k -n "$ns" logs -l app --all-containers --tail=20 --max-log-requests=5 >&2 || true
done
