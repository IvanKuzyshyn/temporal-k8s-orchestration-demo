#!/usr/bin/env bash
# Fast Helm/graph checks: no Kubernetes API or Docker daemon required.
set -euo pipefail
cd "$(dirname "$0")/.."
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

helm lint k8s/worker-helm-chart -f k8s/worker-helm-chart/values-static.yaml --strict
helm template temporal-demo k8s/worker-helm-chart \
  -f k8s/worker-helm-chart/values-static.yaml \
  --kube-version 1.35.8 >"$tmp/apps.yaml"
go run ./cmd/validate-graph <"$tmp/apps.yaml"

for controller in keda kyverno; do
  bash scripts/render-controller.sh "$controller" >"$tmp/$controller.yaml"
done
