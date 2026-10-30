#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/kind-common.sh"
assert_context
tmp=$(mktemp -d)
trap 'rc=$?; rm -rf "$tmp"; if ((rc!=0)); then diagnostics; fi; exit "$rc"' EXIT
trap 'exit 143' TERM
trap 'exit 130' INT
ensure_namespace() {
  local existing
  existing=$(k get namespace "$1" --ignore-not-found -o name)
  if [[ -z $existing ]]; then k create namespace "$1"; fi
}
for ns in temporal keda kyverno; do ensure_namespace "$ns"; done
# Existing credentials are authoritative. Never rotate a password behind a retained PVC.
secret=$(k -n temporal get secret postgres-credentials --ignore-not-found -o name)
pvc=$(k -n temporal get pvc data-postgres-0 --ignore-not-found -o name)
if [[ -z $secret ]]; then
  [[ -z $pvc ]] || { echo 'Retained PostgreSQL PVC has no paired Secret; restore credentials, refusing password rotation' >&2; exit 1; }
  (umask 077; openssl rand -hex 24 | tr -d '\n' > "$tmp/password")
  k -n temporal create secret generic postgres-credentials --from-file="POSTGRES_PASSWORD=$tmp/password" --dry-run=client -o json | jq '.metadata.labels["temporal-demo.io/owner"]="temporal-demo" | .immutable=true' > "$tmp/secret.json"
  k create -f "$tmp/secret.json"
else
  k -n temporal get secret postgres-credentials -o json | jq -e '.metadata.labels["temporal-demo.io/owner"]=="temporal-demo" and (.data.POSTGRES_PASSWORD | length)>0' >/dev/null
  echo 'Reusing installed PostgreSQL Secret (no credential rotation)'
fi
# Reuse the exact schema and native server config already exercised by Compose.
k -n temporal create configmap postgres-init --from-file=10-visibility.sh=deploy/temporal/init-db.sh --dry-run=client -o yaml > "$tmp/init.yaml"
k -n temporal create configmap temporal-scripts --from-file=deploy/temporal/schema.sh --from-file=deploy/temporal/namespace.sh --dry-run=client -o yaml > "$tmp/scripts.yaml"
k -n temporal create configmap temporal-config --from-file=deploy/temporal/config.yaml --dry-run=client -o yaml > "$tmp/config.yaml"
bash scripts/validate-manifests.sh "$tmp/init.yaml" "$tmp/scripts.yaml" "$tmp/config.yaml" k8s/infrastructure/postgresql.yaml k8s/infrastructure/schema-job.yaml k8s/infrastructure/namespace-job.yaml k8s/infrastructure/temporal.yaml
k apply -f "$tmp/init.yaml" -f "$tmp/scripts.yaml" -f "$tmp/config.yaml" -f k8s/infrastructure/postgresql.yaml
k -n temporal rollout status statefulset/postgres --timeout=180s
bash scripts/schema-rerun.sh
k apply -f k8s/infrastructure/temporal.yaml
k -n temporal rollout status deployment/temporal --timeout=180s
k -n temporal delete job temporal-namespace --ignore-not-found --wait=true --timeout=60s
k apply -f k8s/infrastructure/namespace-job.yaml
k -n temporal wait --for=condition=complete job/temporal-namespace --timeout=150s
k -n temporal rollout status deployment/temporal-ui --timeout=120s
# The pinned archives include dependencies. Verify bytes on every install/rerun.
mkdir -p .local/charts
for name in keda kyverno; do
  if [[ $name == keda ]]; then version=$KEDA_CHART_VERSION; sha=$KEDA_CHART_SHA256; repo=https://kedacore.github.io/charts; else version=$KYVERNO_CHART_VERSION; sha=$KYVERNO_CHART_SHA256; repo=https://kyverno.github.io/kyverno; fi
  chart=".local/charts/$name-$version.tgz"
  if [[ ! -f $chart ]]; then
    curl --fail --silent --show-error --location --connect-timeout 10 --max-time 90 --retry 1 --output "$tmp/chart.tgz" "$repo/$name-$version.tgz"
    mv "$tmp/chart.tgz" "$chart"
  fi
  actual=$(openssl dgst -sha256 "$chart"); [[ ${actual##* } == "$sha" ]] || { echo "Chart checksum mismatch: $name" >&2; exit 1; }
  kh upgrade --install "$name" "$chart" --namespace "$name" -f "k8s/infrastructure/$name-values.yaml" --wait --timeout 240s
done
k wait --for=condition=Established crd/scaledobjects.keda.sh crd/triggerauthentications.keda.sh crd/clusterpolicies.kyverno.io crd/policies.kyverno.io --timeout=120s
k -n keda rollout status deployment/keda-operator --timeout=120s
k -n keda rollout status deployment/keda-operator-metrics-apiserver --timeout=120s
k -n keda rollout status deployment/keda-admission-webhooks --timeout=120s
for component in admission background cleanup reports; do k -n kyverno rollout status "deployment/kyverno-$component-controller" --timeout=120s; done
# Deployment readiness includes ready webhook endpoints; also wait for injected CA bundles.
ready=false
for ((i=0;i<30;i++)); do
  k get validatingwebhookconfigurations,mutatingwebhookconfigurations -o json > "$tmp/webhooks.json"
  if jq -e -f scripts/webhooks-ready.jq "$tmp/webhooks.json" >/dev/null; then ready=true; break; fi
  sleep 1
done
[[ $ready == true ]] || { echo 'Webhook CA readiness timed out' >&2; exit 1; }
echo 'PASS: PostgreSQL -> schema -> Temporal health/default namespace; pinned controllers/CRDs/webhooks ready (no policies/scaling installed)'
