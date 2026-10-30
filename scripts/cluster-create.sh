#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/kind-common.sh"
mkdir -p .local
clusters=$(kind get clusters)
if grep -Fxq "$CLUSTER" <<< "$clusters"; then
  assert_context
  echo 'Owned temporal-demo cluster already exists'; exit 0
fi
[[ ! -f $KUBECONFIG && ! -f .local/kind-cluster-uid ]] || { echo 'Stale ownership files: investigate before creating cluster' >&2; exit 1; }
# --kubeconfig prevents kind from writing/merging the global kubeconfig.
kind create cluster --name "$CLUSTER" --image "$KIND_NODE_IMAGE" --config k8s/kind/config.yaml --kubeconfig "$KUBECONFIG" --wait 180s
chmod 600 "$KUBECONFIG"
raw_k get namespace kube-system -o jsonpath='{.metadata.uid}' > .local/kind-cluster-uid
assert_context
k wait --for=condition=Ready node/temporal-demo-control-plane --timeout=120s
