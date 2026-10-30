#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/kind-common.sh"
assert_context
mode=${1:-}
[[ $mode == baseline || $mode == persistence ]] || { echo 'Usage: kind-integration.sh baseline|persistence' >&2; exit 2; }
fixture=$(mktemp -d)
forward_pid=''
stop_forward() {
  if [[ -n $forward_pid ]]; then
    kill "$forward_pid" 2>/dev/null || true
    for ((i=0;i<25;i++)); do if ! kill -0 "$forward_pid" 2>/dev/null; then break; fi; sleep 0.2; done
    if kill -0 "$forward_pid" 2>/dev/null; then kill -KILL "$forward_pid" 2>/dev/null || true; fi
    wait "$forward_pid" 2>/dev/null || true
    forward_pid=''
  fi
}
cleanup() {
  rc=$?
  stop_forward
  if ((rc!=0)); then diagnostics || true; fi
  rm -rf "$fixture"
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 143' TERM
trap 'exit 130' INT
start_forward() {
  assert_context
  # Let kubectl bind an available local port atomically; no check-then-bind race.
  kubectl --context "$CONTEXT" --request-timeout=15s -n temporal-demo-orchestrator port-forward --address 127.0.0.1 service/orchestrator :8080 > "$fixture/forward.log" 2>&1 &
  forward_pid=$!
  local port=''
  for ((i=0;i<60;i++)); do
    kill -0 "$forward_pid" 2>/dev/null || { echo 'Owned port-forward exited' >&2; return 1; }
    port=$(awk '/Forwarding from 127.0.0.1:/ {split($3,a,":"); print a[2]; exit}' "$fixture/forward.log")
    if [[ $port =~ ^[0-9]+$ ]]; then export API_URL="http://127.0.0.1:$port"; echo "Owned API tunnel: $API_URL (PID=$forward_pid)"; return 0; fi
    sleep 0.2
  done
  echo 'Owned port-forward timed out' >&2; return 1
}
wait_api() {
  local ready=false deadline=$((SECONDS+120))
  while ((SECONDS<deadline)); do
    if curl --fail --silent --max-time 2 "$API_URL/readyz" >/dev/null 2>&1; then ready=true; break; fi
    sleep 1
  done
  [[ $ready == true ]] || { echo 'API recovery timed out' >&2; return 1; }
}
assert_preserved() {
  wait_api
  curl --fail --silent --show-error --max-time 10 "$API_URL/runs/$id" > "$fixture/current.json"
  cmp "$fixture/original.json" "$fixture/current.json"
  code=$(curl --silent --show-error --max-time 10 -X POST -H "Idempotency-Key: $key" -o "$fixture/replay.json" -w '%{http_code}' "$API_URL/runs")
  [[ $code == 202 ]] || { echo "Replay returned $code" >&2; return 1; }
  cmp "$fixture/original.json" "$fixture/replay.json"
  echo "PASS: $1 preserves workflowId/runId/planHash/result + keyed replay"
}
start_forward
key="kind-$mode-$(date +%s)-$$-$(openssl rand -hex 4)"
SMOKE_RUNTIME=kind SMOKE_RESTART=0 SMOKE_KEY="$key" SMOKE_RESULT_FILE="$fixture/original.json" bash scripts/compose-smoke.sh
id=$(jq -r .workflowId "$fixture/original.json")
if [[ $mode == baseline ]]; then
  stop_forward
  k -n temporal-demo-orchestrator rollout restart deployment/orchestrator
  k -n temporal-demo-orchestrator rollout status deployment/orchestrator --timeout=120s
  start_forward
  assert_preserved 'Kubernetes orchestrator restart'
else
  pvc_before=$(k -n temporal get pvc data-postgres-0 -o jsonpath='{.metadata.uid}:{.spec.volumeName}')
  secret_before=$(k -n temporal get secret postgres-credentials -o jsonpath='{.metadata.uid}:{.metadata.resourceVersion}')
  pg_before=$(k -n temporal get pod postgres-0 -o jsonpath='{.metadata.uid}')
  temporal_before=$(k -n temporal get pods -l app=temporal -o jsonpath='{.items[0].metadata.uid}')
  # Remove both old Pods, retaining the StatefulSet claim and immutable Secret.
  k -n temporal scale deployment/temporal --replicas=0
  k -n temporal wait --for=delete pod -l app=temporal --timeout=90s
  k -n temporal delete pod postgres-0 --wait=true --timeout=90s
  k -n temporal rollout status statefulset/postgres --timeout=180s
  k -n temporal wait --for=condition=Ready pod/postgres-0 --timeout=180s
  pg_after=$(k -n temporal get pod postgres-0 -o jsonpath='{.metadata.uid}')
  [[ -n $pg_after && $pg_before != "$pg_after" ]] || { echo 'PostgreSQL Pod was not replaced' >&2; exit 1; }
  bash scripts/schema-rerun.sh
  k -n temporal scale deployment/temporal --replicas=1
  k -n temporal rollout status deployment/temporal --timeout=180s
  temporal_after=$(k -n temporal get pods -l app=temporal -o jsonpath='{.items[0].metadata.uid}')
  [[ -n $temporal_after && $temporal_before != "$temporal_after" ]] || { echo 'Temporal Pod was not replaced' >&2; exit 1; }
  [[ $pvc_before == "$(k -n temporal get pvc data-postgres-0 -o jsonpath='{.metadata.uid}:{.spec.volumeName}')" ]] || exit 1
  [[ $secret_before == "$(k -n temporal get secret postgres-credentials -o jsonpath='{.metadata.uid}:{.metadata.resourceVersion}')" ]] || exit 1
  assert_preserved 'Temporal/PostgreSQL Pod replacement + safe schema rerun'
  default_version=$(k -n temporal exec postgres-0 -- psql -U temporal -d temporal -Atc 'SELECT curr_version FROM schema_version')
  visibility_version=$(k -n temporal exec postgres-0 -- psql -U temporal -d temporal_visibility -Atc 'SELECT curr_version FROM schema_version')
  [[ $default_version == 1.19 && $visibility_version == 1.14 ]] || { echo 'Unexpected stored schema versions' >&2; exit 1; }
  echo 'PASS: same PVC UID/bound volume and immutable Secret UID/resourceVersion; schemas default1.19/visibility1.14'
fi
echo "PASS: kind $mode; owned tunnel and temporary files will be removed; cluster/PVC retained"
