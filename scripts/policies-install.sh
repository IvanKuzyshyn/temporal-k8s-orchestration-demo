#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/kind-common.sh"
assert_context
for component in admission background reports cleanup; do
  k -n kyverno rollout status "deployment/kyverno-$component-controller" --timeout=90s
done
# Refuse collisions before any mutation. Never touch unrelated admission policy.
for resource in validatingpolicy.policies.kyverno.io/temporal-validate-workers generatingpolicy.policies.kyverno.io/temporal-generate-registrations clusterrole/temporal-worker-service-reader; do
  existing=$(k get "$resource" --ignore-not-found -o json)
  if [[ -n $existing ]] && ! jq -e '.metadata.labels["app.kubernetes.io/part-of"] == "temporal-worker-discovery"' <<<"$existing" >/dev/null; then
    echo "Refusing foreign $resource" >&2
    exit 1
  fi
done
k apply --dry-run=server --validate=strict -f k8s/policies/rbac.yaml
k apply -f k8s/policies/rbac.yaml
# Aggregation is asynchronous. Check the actual identities, not the label alone.
ready=false
for ((i = 0; i < 60; i++)); do
  ready=true
  for component in admission background reports; do
    if [[ $(k auth can-i list services --all-namespaces --as="system:serviceaccount:kyverno:kyverno-$component-controller") != yes ]]; then
      ready=false
    fi
  done
  if [[ $ready == true ]]; then
    break
  fi
  sleep 1
done
[[ $ready == true ]] || {
  echo 'Kyverno Service reader aggregation did not converge' >&2
  exit 1
}
for file in validate-workers generate-registrations; do
  k apply --dry-run=server --validate=strict -f "k8s/policies/$file.yaml"
  k apply -f "k8s/policies/$file.yaml"
done
k wait --for='jsonpath={.status.conditionStatus.ready}=true' validatingpolicy.policies.kyverno.io/temporal-validate-workers generatingpolicy.policies.kyverno.io/temporal-generate-registrations --timeout=90s
