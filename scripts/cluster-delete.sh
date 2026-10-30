#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/kind-common.sh"
clusters=$(kind get clusters)
if ! grep -Fxq "$CLUSTER" <<< "$clusters"; then
  # Absent is idempotent, but never delete ownership files of an unknown cluster.
  echo 'temporal-demo cluster absent'; exit 0
fi
assert_context
kind delete cluster --name "$CLUSTER" --kubeconfig "$KUBECONFIG"
rm -f "$KUBECONFIG" .local/kind-cluster-uid
echo 'Deleted ONLY temporal-demo, including its local PostgreSQL PVC history'
