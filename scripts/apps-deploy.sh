#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/kind-common.sh"
assert_context
[[ -f .local/kind-image-tag ]] || { echo 'Run images:load first' >&2; exit 1; }
tag=$(< .local/kind-image-tag)
[[ -n $tag && $tag != latest && $tag != render-only ]] || exit 1
tmp=$(mktemp -d)
trap 'rc=$?; rm -rf "$tmp"; if ((rc!=0)); then diagnostics; fi; exit "$rc"' EXIT
trap 'exit 143' TERM
trap 'exit 130' INT
helm template temporal-demo k8s/apps -f k8s/apps/values-static.yaml --set-string "images.tag=$tag" --kube-version 1.35.8 > "$tmp/apps.yaml"
go run ./cmd/validate-graph < "$tmp/apps.yaml"
bash scripts/validate-manifests.sh "$tmp/apps.yaml"
# Helm owns only the single application chart; explicit namespaces live in render.
kh upgrade --install temporal-demo k8s/apps --namespace temporal-demo-orchestrator --create-namespace -f k8s/apps/values-static.yaml --set-string "images.tag=$tag" --wait --timeout 180s
# Target only replicas present in the static overlay, not future zero-replica sets.
k -n temporal-demo-orchestrator rollout status deployment/orchestrator --timeout=120s
for worker in worker-a worker-b worker-c worker-d; do k -n "temporal-demo-$worker" rollout status "deployment/$worker" --timeout=120s; done
