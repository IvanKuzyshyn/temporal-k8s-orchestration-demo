#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"
export COMPOSE_PROJECT_NAME="${COMPOSE_PROJECT_NAME:-temporal-demo}"
export COMPOSE_ENV_FILE="${COMPOSE_ENV_FILE:-$ROOT/deploy/.env}"
if [[ -z ${APP_IMAGE_TAG:-} && -f .local/compose-image-tag ]]; then
  APP_IMAGE_TAG=$(< .local/compose-image-tag)
  export APP_IMAGE_TAG
fi
# --env-file is opt-in so credential-free config validation needs no local file.
dc() {
  if [[ -f $COMPOSE_ENV_FILE ]]; then
    docker compose --env-file "$COMPOSE_ENV_FILE" -p "$COMPOSE_PROJECT_NAME" -f deploy/compose.yaml "$@"
  else
    docker compose -p "$COMPOSE_PROJECT_NAME" -f deploy/compose.yaml "$@"
  fi
}
credentials() {
  if [[ ! -f $COMPOSE_ENV_FILE ]]; then
    mkdir -p "$(dirname "$COMPOSE_ENV_FILE")"
    (umask 077; printf 'POSTGRES_PASSWORD=%s\n' "$(openssl rand -hex 24)" > "$COMPOSE_ENV_FILE")
  fi
  # Never source env files as shell code, echo their contents, or render config.
  if ! grep -Eq '^POSTGRES_PASSWORD=[a-fA-F0-9]{32,}$' "$COMPOSE_ENV_FILE"; then
    echo 'Set POSTGRES_PASSWORD to a random hex value (at least32 digits) in the local env file' >&2; return 1
  fi
}
diagnostics() {
  echo "Scoped diagnostics: project=$COMPOSE_PROJECT_NAME" >&2
  dc ps --all --format json | jq -c '{Name,Service,State,Health,ExitCode}' >&2 || true
  # Only application logs: infrastructure connection errors may contain secrets.
  dc logs --no-color --tail 30 orchestrator worker-a worker-b worker-c worker-d >&2 || true
}
wait_apps() {
  dc up -d --no-build --wait --wait-timeout 240
}
