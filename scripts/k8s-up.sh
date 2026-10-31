#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/kind-common.sh"
source k8s/infrastructure/versions.env
mkdir -p .local
clusters=$(kind get clusters)
if ! grep -Fxq "$CLUSTER" <<< "$clusters"; then
  kind create cluster --name "$CLUSTER" --image "$KIND_NODE_IMAGE" --config k8s/infrastructure/kind.yaml --kubeconfig "$KUBECONFIG" --wait 180s
  chmod 600 "$KUBECONFIG"
fi
assert_context
work=$(mktemp -d)
trap 'rc=$?; rm -rf "$work"; if ((rc!=0)); then bash scripts/kind-diagnostics.sh || true; fi; exit "$rc"' EXIT
trap 'exit 143' TERM
trap 'exit 130' INT
# Render and strictly validate once before any Kyverno mutations.
bash scripts/render-kyverno.sh > "$work/kyverno.yaml"
bash scripts/validate-manifests.sh "$work/kyverno.yaml"
k wait --for=condition=Ready node/temporal-demo-control-plane --timeout=120s
# Labels let kubectl apply ordinary manifests in explicit bootstrap phases.
k apply -f k8s/infrastructure/temporal.yaml -f k8s/infrastructure/keda.yaml -l temporal-demo.io/phase=namespace
k apply -f "$work/kyverno.yaml" -l temporal-demo.io/phase=namespace
secret=$(k -n temporal get secret postgres-credentials --ignore-not-found -o name)
pvc=$(k -n temporal get pvc data-postgres-0 --ignore-not-found -o name)
if [[ -z $secret ]]; then
  [[ -z $pvc ]] || { echo 'Retained PostgreSQL PVC has no paired Secret; restore credentials, refusing password rotation' >&2; exit 1; }
  (umask 077; openssl rand -hex 24 | tr -d '\n' > "$work/password")
  k -n temporal create secret generic postgres-credentials --from-file="POSTGRES_PASSWORD=$work/password" --dry-run=client -o json | jq '.immutable=true' > "$work/secret.json"
  k create -f "$work/secret.json"
else
  k -n temporal get secret postgres-credentials -o json | jq -e '.immutable==true and (.data.POSTGRES_PASSWORD | length)>0' >/dev/null
  echo 'Reusing installed immutable PostgreSQL Secret (no credential rotation)'
fi
k apply -f k8s/infrastructure/temporal.yaml -l temporal-demo.io/phase=postgres
k -n temporal rollout status statefulset/postgres --timeout=180s
# Jobs have immutable templates: recreate only the completed/failed bootstrap Jobs,
# never PostgreSQL, its credentials or storage. Execution is sequential.
k -n temporal delete job temporal-schema temporal-namespace --ignore-not-found --wait=true --timeout=60s
k apply -f k8s/infrastructure/temporal.yaml -l temporal-demo.io/phase=schema
k -n temporal wait --for=condition=complete job/temporal-schema --timeout=270s
k apply -f k8s/infrastructure/temporal.yaml -l temporal-demo.io/phase=server
k -n temporal rollout status deployment/temporal --timeout=180s
k apply -f k8s/infrastructure/temporal.yaml -l temporal-demo.io/phase=namespace-job
k -n temporal wait --for=condition=complete job/temporal-namespace --timeout=150s
k apply -f k8s/infrastructure/temporal.yaml -l temporal-demo.io/phase=ui
k -n temporal rollout status deployment/temporal-ui --timeout=120s
# CRDs exceed client-side annotation limits. SSA shares matching Helm-owned
# fields without forcing conflicts. Ordinary resources use client-side apply,
# avoiding SSA conflicts on API-defaulted fields from the old Helm install.
k apply --server-side --field-manager=temporal-demo -f k8s/infrastructure/keda.yaml -l temporal-demo.io/phase=crds
k apply --server-side --field-manager=temporal-demo -f "$work/kyverno.yaml" -l temporal-demo.io/phase=crds
k wait --for=condition=Established crd -l temporal-demo.io/phase=crds --timeout=120s
k apply -f k8s/infrastructure/keda.yaml -l temporal-demo.io/phase=controller
k apply -f "$work/kyverno.yaml" -l 'temporal-demo.io/phase notin (crds,namespace)'
for name in keda-operator keda-operator-metrics-apiserver keda-admission-webhooks; do
  k -n keda rollout status "deployment/$name" --timeout=180s
done
for component in admission background cleanup reports; do
  k -n kyverno rollout status "deployment/kyverno-$component-controller" --timeout=180s
done
ready=false
for ((i=0;i<60;i++)); do
  k get validatingwebhookconfigurations,mutatingwebhookconfigurations -o json > "$work/webhooks.json"
  if jq -e -f scripts/webhooks-ready.jq "$work/webhooks.json" >/dev/null; then ready=true; break; fi
  sleep 1
done
[[ $ready == true ]] || { echo 'Webhook CA readiness timed out' >&2; exit 1; }
# Build directly, without invoking Compose or sharing its image-tag state.
tag=$(git rev-parse HEAD)
if [[ -n $(git status --porcelain) ]]; then tag="$tag-local-$(date +%s)-$(openssl rand -hex 4)"; fi
docker build -f deploy/orchestrator.Dockerfile -t "temporal-demo/orchestrator:$tag" .
docker build -f deploy/worker.Dockerfile -t "temporal-demo/worker:$tag" .
kind load docker-image --name "$CLUSTER" "temporal-demo/orchestrator:$tag" "temporal-demo/worker:$tag"
helm template temporal-demo k8s/worker-helm-chart -f k8s/worker-helm-chart/values-static.yaml --set-string "images.tag=$tag" --kube-version 1.35.8 > "$work/apps.yaml"
go run ./cmd/validate-graph < "$work/apps.yaml"
bash scripts/validate-manifests.sh "$work/apps.yaml"
# Apply exactly the validated bytes, not a second Helm render/release.
k apply -f "$work/apps.yaml"
k -n temporal-demo-orchestrator rollout status deployment/orchestrator --timeout=180s
for worker in worker-a worker-b worker-c worker-d; do
  k -n "temporal-demo-$worker" rollout status "deployment/$worker" --timeout=180s
done
echo 'PASS: temporal-demo ready; fixed replicas, static registry, no policies/scaling; history retained'
