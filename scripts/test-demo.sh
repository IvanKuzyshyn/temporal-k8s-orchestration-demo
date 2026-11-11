#!/usr/bin/env bash
# Destructive only to a new cluster created by this invocation. Never reuse one.
set -euo pipefail
source "$(dirname "$0")/kind-common.sh"
nodes=$(docker ps -a --filter "label=io.x-k8s.kind.cluster=$CLUSTER" --format '{{.Names}}')
[[ -z $nodes && ! -e $KUBECONFIG && ! -e .local/lifecycle/node-id && ! -e .local/lifecycle.lock ]] || {
  echo 'Integration test requires an unused demo cluster name and clean local lifecycle state; refusing existing resources' >&2; exit 1;
}
cleanup() {
  rc=$?
  trap - EXIT
  if [[ -s .local/lifecycle/node-id ]]; then
    if ((rc != 0)); then bash scripts/kind-diagnostics.sh || true; fi
    mise run destroy || rc=1
    mise run destroy || rc=1
  fi
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
mise run up
export DEMO_ROOT="$ROOT" DEMO_API_URL="http://127.0.0.1:${API_FORWARD_PORT:-18080}"
go test -tags=demo_integration -count=1 -run '^TestDemoLifecycle$' -v -timeout=20m ./tests/integration
