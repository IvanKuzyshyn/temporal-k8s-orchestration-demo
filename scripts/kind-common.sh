#!/usr/bin/env bash
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
source k8s/infrastructure/versions.env
raw_k() { kubectl --context "$CONTEXT" --request-timeout=15s "$@"; }
assert_context() {
  [[ -f $KUBECONFIG && -f .local/kind-cluster-uid ]] || { echo 'Missing owned kind kubeconfig/UID; run cluster:create' >&2; return 1; }
  local config node owner uid
  config=$(kubectl --context "$CONTEXT" config view -o json) || return
  jq -e --arg context "$CONTEXT" '."current-context" == $context and (.contexts | length)==1 and .contexts[0].name==$context and .contexts[0].context.cluster==$context and (.clusters | length)==1 and .clusters[0].name==$context and (.clusters[0].cluster.server | startswith("https://127.0.0.1:"))' <<< "$config" >/dev/null || { echo 'Wrong kind context/cluster; refusing mutation' >&2; return 1; }
  node=$(kind get nodes --name "$CLUSTER") || return
  [[ $node == temporal-demo-control-plane ]] || { echo 'Wrong cluster nodes; refusing mutation' >&2; return 1; }
  owner=$(docker inspect --format '{{index .Config.Labels "io.x-k8s.kind.cluster"}}' "$node") || return
  [[ $owner == "$CLUSTER" ]] || { echo 'Wrong Docker kind ownership' >&2; return 1; }
  uid=$(raw_k get namespace kube-system -o jsonpath='{.metadata.uid}') || return
  [[ -n $uid && $uid == "$(< .local/kind-cluster-uid)" ]] || { echo 'Wrong cluster UID; refusing mutation' >&2; return 1; }
}
# Every mutation goes through these guards, including Helm, pod deletion and exec.
k() { assert_context || return; raw_k "$@"; }
kh() { assert_context || return; helm --kube-context "$CONTEXT" "$@"; }
diagnostics() {
  assert_context || return
  echo "Scoped diagnostics: context=$CONTEXT (no Secrets or infrastructure logs)" >&2
  raw_k get pods,pvc -A >&2 || true
  raw_k get events -A --field-selector type=Warning >&2 || true
  for ns in temporal-demo-orchestrator temporal-demo-worker-a temporal-demo-worker-b temporal-demo-worker-c temporal-demo-worker-d; do
    raw_k -n "$ns" logs -l app --all-containers --tail=20 --max-log-requests=5 >&2 || true
  done
}
