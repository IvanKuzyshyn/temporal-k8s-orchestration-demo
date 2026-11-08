#!/usr/bin/env bash
# Validate only; never bootstrap Namespaces, install controllers or create a cluster.
set -euo pipefail
source "$(dirname "$0")/kind-common.sh"
assert_context
if (($#)); then
  k apply --dry-run=server --validate=strict -f "$1"
else
  # Applications are native resources; explicitly require the installed custom APIs too.
  k wait --for=condition=Established crd/scaledobjects.keda.sh crd/clusterpolicies.kyverno.io --timeout=1s
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  helm template temporal-demo k8s/worker-helm-chart -f k8s/worker-helm-chart/values-static.yaml --kube-version 1.35.8 > "$tmp/apps.yaml"
  go run ./cmd/validate-graph < "$tmp/apps.yaml"
  k apply --dry-run=server --validate=strict -f "$tmp/apps.yaml"
fi
