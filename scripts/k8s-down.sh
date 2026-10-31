#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/kind-common.sh"
clusters=$(kind get clusters)
if ! grep -Fxq "$CLUSTER" <<< "$clusters"; then
  echo 'temporal-demo cluster absent'; exit 0
fi
assert_context
# Explicit, destructive opt-in: delete this named demo and ALL its PVC history.
kind delete cluster --name "$CLUSTER" --kubeconfig "$KUBECONFIG"
rm -f "$KUBECONFIG" .local/kind-cluster-uid
echo 'Deleted ONLY temporal-demo, including its local PostgreSQL PVC history'
