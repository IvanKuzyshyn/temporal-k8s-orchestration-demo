#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
# Require an explicit job identity: this entrypoint must never default to the
# ordinary demo or discover/reset projects outside the exact job prefix.
job="${COMPOSE_PROJECT_NAME:-}"
[[ $job =~ ^temporal-demo-ci-[0-9]+-[0-9]+$ ]] || {
  echo 'Cleanup requires COMPOSE_PROJECT_NAME=temporal-demo-ci-<run-id>-<attempt>' >&2
  exit 1
}
status=0
fixtures=''
if ! listing=$(docker compose ls --all --format json); then
  echo 'Compose fixture discovery failed; still attempting normal job cleanup' >&2
  status=1
elif ! fixtures=$(printf '%s\n' "$listing" | jq -r --arg prefix "$job-persistence-" '
  if type != "array" then error("expected project array")
  elif any(.[]; (.Name | type) != "string") then error("expected project names")
  elif any(.[]; (.Name | test("^[a-z0-9][a-z0-9_-]*$")) | not) then error("invalid project name")
  else .[] | .Name | select(startswith($prefix)) end
'); then
  echo 'Compose fixture discovery returned invalid output; still attempting normal job cleanup' >&2
  status=1
fi
if [[ -n $fixtures ]]; then
  while IFS= read -r project; do
    if ! COMPOSE_PROJECT_NAME="$project" RESET_CONFIRM=1 bash scripts/compose.sh reset; then
      echo "Fixture cleanup failed: $project" >&2
      status=1
    fi
  done <<< "$fixtures"
fi
# Attempt this even after failed discovery or any failed fixture reset.
if ! COMPOSE_PROJECT_NAME="$job" RESET_CONFIRM=1 bash scripts/compose.sh reset; then
  echo "Normal job cleanup failed: $job" >&2
  status=1
fi
exit "$status"
