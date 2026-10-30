#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"
mkdir -p .local/schema-cache
# Immutable upstream revisions; never default to mutable master or skip unknown kinds.
CORE=47163ead3738a996c3e30be9691bb37b8071d9d3
validate() {
  kubeconform -strict -summary -kubernetes-version 1.35.8 -cache "$ROOT/.local/schema-cache" \
    -schema-location "https://raw.githubusercontent.com/yannh/kubernetes-json-schema/$CORE/{{.NormalizedKubernetesVersion}}-standalone{{.StrictSuffix}}/{{.ResourceKind}}{{.KindSuffix}}.json" \
    -schema-location "$ROOT/k8s/schemas/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json" "$@"
}
if (($#)); then validate "$@"; exit; fi
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
helm lint k8s/worker-helm-chart -f k8s/worker-helm-chart/values-static.yaml --strict
helm template temporal-demo k8s/worker-helm-chart -f k8s/worker-helm-chart/values-static.yaml --kube-version 1.35.8 > "$tmp/apps.yaml"
go run ./cmd/validate-graph < "$tmp/apps.yaml"
validate "$tmp/apps.yaml" k8s/infrastructure/temporal.yaml k8s/infrastructure/keda.yaml k8s/infrastructure/kyverno.yaml k8s/schema-tests/custom.yaml
# Assert failure locally and in every CI consumer (never mask a validator failure).
if validate k8s/schema-tests/unknown.yaml > "$tmp/unknown.log" 2>&1; then
  echo 'FAIL: strict schema validator accepted unknown kind' >&2; exit 1
fi
grep -q 'could not find schema' "$tmp/unknown.log"
echo 'PASS: unknown kind rejected by strict schema validation'
