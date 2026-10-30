#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
base="${API_URL:-${COMPOSE_API_URL:-http://127.0.0.1:8080}}"
runtime="${SMOKE_RUNTIME:-compose}"
[[ $runtime == compose || $runtime == kind ]] || { echo 'Unknown smoke runtime' >&2; exit 1; }
if [[ $runtime == kind && ${SMOKE_RESTART:-0} != 0 ]]; then
  echo 'Kind orchestrator restarts belong to the owned integration driver' >&2; exit 1
fi
wait_seconds="${SMOKE_WAIT_SECONDS:-180}"
tmp=$(mktemp -d)
cleanup() {
  rc=$?
  if ((rc!=0)); then
    if [[ $runtime == kind ]]; then bash scripts/kind-diagnostics.sh || true; else bash scripts/compose.sh diagnostics || true; fi
  fi
  rm -rf "$tmp"
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 143' TERM
trap 'exit 130' INT
request() { curl --silent --show-error --max-time 5 "$@"; }
fail() { echo "FAIL: $*" >&2; exit 1; }
[[ $wait_seconds =~ ^[1-9][0-9]*$ ]] || fail 'SMOKE_WAIT_SECONDS must be a positive integer'
wait_seconds=$((10#$wait_seconds))
# Bash SECONDS is elapsed time (one-second resolution), not an attempt count.
# Cap every polling request and sleep by the remaining phase budget. Allow at
# most one clock tick of scheduling granularity, plus failure diagnostics.
poll_request() {
  local remaining=$((deadline - SECONDS))
  ((remaining > 0)) || return 1
  if ((remaining > 5)); then remaining=5; fi
  request --max-time "$remaining" "$@"
}
poll_pause() {
  if ((SECONDS < deadline)); then sleep 1; fi
}
ready=false
deadline=$((SECONDS + wait_seconds))
while ((SECONDS < deadline)); do
  if poll_request --fail "$base/readyz" > /dev/null 2>&1; then ready=true; break; fi
  poll_pause
done
if [[ $ready != true ]]; then echo "FAIL: API readiness did not succeed within ${wait_seconds}s" >&2; exit 1; fi
key="${SMOKE_KEY:-smoke-$(date +%s)-$$-${RANDOM}}"
code=$(request -o "$tmp/start" -w '%{http_code}' -X POST -H "Idempotency-Key: $key" "$base/runs")
[[ $code == 202 ]] || fail "start returned $code, expected202"
jq -e '.workflowId != "" and .runId != "" and (.planHash | test("^[a-f0-9]{64}$"))' "$tmp/start" >/dev/null
id=$(jq -r .workflowId "$tmp/start")
complete=false
deadline=$((SECONDS + wait_seconds))
while ((SECONDS < deadline)); do
  if poll_request --fail "$base/runs/$id" > "$tmp/result"; then
    status=$(jq -r .status "$tmp/result")
    if [[ $status == completed ]]; then complete=true; break; fi
    if [[ $status != running ]]; then fail "run ended with $status"; fi
  fi
  poll_pause
done
[[ $complete == true ]] || fail "run did not complete within ${wait_seconds}s"
jq -e '.result == {"results":[{"workerName":"worker-a"},{"workerName":"worker-b"},{"workerName":"worker-c"},{"workerName":"worker-d"}]}' "$tmp/result" >/dev/null
# Both source registry and durable result must agree on the selected plan.
request --fail "$base/workers" > "$tmp/workers"
jq -e --slurpfile start "$tmp/start" '.plan.hash == $start[0].planHash' "$tmp/workers" >/dev/null
jq -e --slurpfile start "$tmp/start" '.workflowId == $start[0].workflowId and .runId == $start[0].runId and .planHash == $start[0].planHash' "$tmp/result" >/dev/null
replay() {
  code=$(request -o "$tmp/replay" -w '%{http_code}' -X POST -H "Idempotency-Key: $key" "$base/runs") || fail 'replay transport error'
  [[ $code == 202 ]] || fail "replay returned $code, expected202"
  cmp "$tmp/result" "$tmp/replay"
}
replay
if [[ $runtime == compose && ${SMOKE_RESTART:-1} == 1 ]]; then
  bash scripts/compose.sh restart-app
  # Docker can reassign an ephemeral published port when restarting a container.
  base="http://$(bash scripts/compose.sh api-address)"
  replay
  echo 'PASS: keyed replay after orchestrator restart'
fi
code=$(request -o "$tmp/bad" -w '%{http_code}' -X POST --data-binary ' ' "$base/runs") || fail 'body rejection transport error'
[[ $code == 400 ]] || fail "unexpected body returned $code, expected400"
if [[ -n ${SMOKE_RESULT_FILE:-} ]]; then cp "$tmp/result" "$SMOKE_RESULT_FILE"; fi
printf 'PASS: readiness, body-free diamond, sorted identity-only results, plan hash, completed keyed replay, body400\n'
jq -c . "$tmp/result"
