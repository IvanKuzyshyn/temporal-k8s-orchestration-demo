#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/compose-common.sh"
# All operations, including destructive reset, target a brand-new test project.
export COMPOSE_PROJECT_NAME="$COMPOSE_PROJECT_NAME-persistence-$(date +%s)-$$-$(openssl rand -hex 3)"
fixture=$(mktemp -d)
export COMPOSE_ENV_FILE="$fixture/.env"
export COMPOSE_API_PORT=0 COMPOSE_TEMPORAL_PORT=0 COMPOSE_UI_PORT=0
cleanup() {
  rc=$?
  if ((rc!=0)); then diagnostics; fi
  dc down --volumes --timeout 40 || rc=1
  rm -rf "$fixture"
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 143' TERM
trap 'exit 130' INT
[[ -n ${APP_IMAGE_TAG:-} ]] || { echo 'Build images first: mise run images:build' >&2; exit 1; }
credentials
wait_apps
api_address() { dc port orchestrator 8080; }
export COMPOSE_API_URL="http://$(api_address)"
SMOKE_RESULT_FILE="$fixture/original.json" bash scripts/compose-smoke.sh
id=$(jq -r .workflowId "$fixture/original.json")
assert_preserved() {
  export COMPOSE_API_URL="http://$(api_address)"
  curl --fail --silent --show-error --max-time 10 "$COMPOSE_API_URL/runs/$id" > "$fixture/current.json"
  cmp "$fixture/original.json" "$fixture/current.json"
  echo "PASS: $1 preserves workflowId/runId/planHash/result"
}
# Actual restart and then container replacement, preserving the named PG volume.
dc stop --timeout 40 temporal postgres
dc start postgres
for ((i=0;i<60;i++)); do
  if dc exec -T postgres pg_isready -U temporal -d temporal >/dev/null 2>&1; then break; fi
  if ((i==59)); then echo 'PostgreSQL restart timed out' >&2; exit 1; fi
  sleep 1
done
dc start temporal
wait_apps
assert_preserved 'Temporal/PostgreSQL restart'
dc stop --timeout 40 temporal postgres
dc rm -f temporal postgres
wait_apps
assert_preserved 'Temporal/PostgreSQL replacement'
# Run both matching schema setups/migrations again against the preserved data.
dc run --rm --no-deps schema
assert_preserved 'repeatable default+visibility schema setup'
dc exec -T postgres psql -U temporal -d temporal -Atc 'SELECT curr_version FROM schema_version'
dc exec -T postgres psql -U temporal -d temporal_visibility -Atc 'SELECT curr_version FROM schema_version'
dc down --timeout 40
wait_apps
export COMPOSE_API_URL="http://$(api_address)"
assert_preserved 'Compose down/up'
# Explicit reset task against ONLY the isolated fixture. Never normal demo data.
RESET_CONFIRM=1 bash scripts/compose.sh reset
wait_apps
export COMPOSE_API_URL="http://$(api_address)"
code=$(curl --silent --show-error --max-time 10 -o "$fixture/missing.json" -w '%{http_code}' "$COMPOSE_API_URL/runs/$id")
[[ $code == 404 ]] || { echo "FAIL: reset returned $code, expected404" >&2; exit 1; }
echo 'PASS: isolated destructive reset removes old history (HTTP404)'
SMOKE_RESTART=0 bash scripts/compose-smoke.sh
printf 'PASS: persistence complete; isolated project=%s will be removed with its volume\n' "$COMPOSE_PROJECT_NAME"
