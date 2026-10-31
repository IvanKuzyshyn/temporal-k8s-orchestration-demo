#!/usr/bin/env bash
# Shared boundary only: no Docker inspection, ownership database or lifecycle framework.
set -euo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"
CLUSTER=temporal-demo
CONTEXT=kind-temporal-demo
OWN_KUBECONFIG="$ROOT/.local/kubeconfig"
if [[ -n ${KUBECONFIG:-} && $KUBECONFIG != "$OWN_KUBECONFIG" ]]; then
  echo 'refusing foreign KUBECONFIG; unset it or select this worktree .local/kubeconfig' >&2; exit 1
fi
export KUBECONFIG="$OWN_KUBECONFIG"
k() { kubectl --kubeconfig "$KUBECONFIG" --context "$CONTEXT" --request-timeout=15s "$@"; }
assert_context() {
  [[ -f $KUBECONFIG && $(kubectl --kubeconfig "$KUBECONFIG" config current-context) == "$CONTEXT" ]] || {
    echo 'Wrong or missing local kind context; expected kind-temporal-demo (run k8s:up)' >&2; return 1;
  }
}
