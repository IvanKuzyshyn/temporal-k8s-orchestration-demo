#!/usr/bin/env bash
# Secret-free Task10 evidence; never reads Secrets or SQL/schema logs.
set -euo pipefail
source "$(dirname "$0")/kind-common.sh"
assert_context
proof=${1:?proof directory required}
[[ -d $proof ]] || exit 1
failed=0
for name in orchestrator worker-a worker-b worker-c worker-d; do
  ns="temporal-demo-$name"
  k -n "$ns" get pods,events,scaledobjects,configmaps -o json >"$proof/diagnostics-$name.json" 2>"$proof/diagnostics-$name.err" || failed=1
  k -n "$ns" logs -l "app=$name" --all-containers --tail=150 --max-log-requests=5 >"$proof/logs-$name.txt" 2>&1 || failed=1
done
if [[ -n ${DEMO_API_URL:-} ]]; then
  curl --silent --show-error --max-time 5 "$DEMO_API_URL/workers" >"$proof/registry.json" || failed=1
fi
if [[ -f ${SCALING_RUNS_FILE:-/nonexistent} ]]; then
  go test -tags=integration,acceptance_integration -count=1 -run '^TestAcceptanceDiagnostics$' -v -timeout=90s ./tests/integration >"$proof/history-diagnostics.log" 2>&1 || failed=1
fi
exit "$failed"
