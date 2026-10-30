#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/kind-common.sh"
assert_context
# Existing helper only builds; it never starts/stops the user Compose project.
COMPOSE_PROJECT_NAME=temporal-demo-kind-build COMPOSE_ENV_FILE="$ROOT/.local/kind-build-no-credentials" bash scripts/compose.sh build
tag=$(< .local/compose-image-tag)
assert_context
kind load docker-image --name "$CLUSTER" "temporal-demo/orchestrator:$tag" "temporal-demo/worker:$tag"
printf '%s\n' "$tag" > .local/kind-image-tag
