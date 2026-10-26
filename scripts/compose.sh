#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/compose-common.sh"
trap 'rc=$?; if ((rc!=0)); then diagnostics; fi' EXIT
build() {
  local sha tag
  sha=$(git rev-parse HEAD)
  if [[ -n $(git status --porcelain) ]]; then
    tag="local-$(date -u +%Y%m%dT%H%M%S)-$(openssl rand -hex 6)"
  else
    tag=$sha
  fi
  export APP_IMAGE_TAG=$tag
  dc build orchestrator worker-a
  mkdir -p .local
  printf '%s\n' "$tag" > .local/compose-image-tag
  echo "Built both app images: $tag"
}
case "${1:-}" in
  config) dc config --quiet ;;
  build) build ;;
  up) credentials; build; wait_apps ;;
  down) dc down --timeout 40 ;;
  reset)
    [[ ${RESET_CONFIRM:-} == 1 ]] || { echo 'Destructive: set RESET_CONFIRM=1 to remove this project and its history volume' >&2; exit 1; }
    dc down --volumes --timeout 40 ;;
  restart-app) dc restart --timeout 40 orchestrator; dc up -d --no-build --no-deps --wait --wait-timeout 90 orchestrator ;;
  api-address) dc port orchestrator 8080 ;;
  diagnostics) diagnostics ;;
  *) echo 'Usage: compose.sh config|build|up|down|reset|restart-app|api-address|diagnostics' >&2; exit 2 ;;
esac
