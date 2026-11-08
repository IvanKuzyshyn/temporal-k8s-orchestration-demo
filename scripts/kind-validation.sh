#!/usr/bin/env bash
# Real API/admission acceptance. Every object is dry-run only, including policies/scalers.
set -euo pipefail
source "$(dirname "$0")/kind-common.sh"
assert_context
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
assert_absent() {
  local objects
  objects=$(k get -f "k8s/server-validation/$1.yaml" --ignore-not-found -o name)
  [[ -z $objects ]] || { echo "FAIL: validation fixture already exists/persisted: $objects" >&2; return 1; }
}
# All positive controls must pass first, proving Namespace/CRD/target/admission readiness.
for name in native scaledobject clusterpolicy; do
  assert_absent "$name"
  bash scripts/validate-k8s.sh "k8s/server-validation/$name.yaml"
  assert_absent "$name"
  echo "PASS: $name valid control; not persisted"
done
for name in native scaledobject clusterpolicy unknown; do
  input=$(<"k8s/server-validation/$name.yaml")
  case "$name" in
    native) input=${input/'replicas: 1'/'replicas: wrong'}; field=replicas ;;
    scaledobject) input=${input/'maxReplicaCount: 1'/'maxReplicaCount: wrong'}; field=maxReplicaCount ;;
    clusterpolicy) input=${input/'name: valid-rule'/'name: 123'}; field='spec.rules[0].name' ;;
    unknown) field='no matches for kind "UnknownValidationControl"' ;;
  esac
  printf '%s\n' "$input" > "$tmp/$name.yaml"
  if bash scripts/validate-k8s.sh "$tmp/$name.yaml" > "$tmp/$name.log" 2>&1; then
    echo "FAIL: API accepted invalid $name" >&2; exit 1
  fi
  cat "$tmp/$name.log"
  grep -Fq "$field" "$tmp/$name.log"
  if [[ $name != unknown ]]; then assert_absent "$name"; fi
  echo "PASS: $name rejected for $field; not persisted"
done
