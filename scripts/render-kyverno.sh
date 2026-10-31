#!/usr/bin/env bash
set -euo pipefail
source k8s/infrastructure/versions.env
cache=.local/charts
archive="$cache/kyverno-$KYVERNO_CHART_VERSION.tgz"
mkdir -p "$cache"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
verify() {
  local actual
  actual=$(openssl dgst -sha256 "$1")
  [[ ${actual##* } == "$KYVERNO_CHART_SHA256" ]] || { echo 'Kyverno archive SHA256 mismatch; refusing render' >&2; return 1; }
}
if [[ ! -f $archive ]]; then
  curl --fail --location --silent --show-error --connect-timeout 15 --max-time 120 --retry 2 --retry-max-time 240 \
    "https://kyverno.github.io/kyverno/kyverno-$KYVERNO_CHART_VERSION.tgz" -o "$work/chart.tgz"
  verify "$work/chart.tgz"
  mv "$work/chart.tgz" "$archive"
fi
# Cached bytes are never trusted implicitly or silently replaced on mismatch.
verify "$archive"
helm template kyverno "$archive" --namespace kyverno --kube-version 1.35.8 \
  --include-crds --no-hooks --skip-tests -f k8s/infrastructure/kyverno-values.yaml > "$work/render.yaml"
# Helm does not emit the release namespace. Emit only after a successful render.
printf 'apiVersion: v1\nkind: Namespace\nmetadata:\n  name: kyverno\n  labels:\n    temporal-demo.io/phase: namespace\n---\n'
cat "$work/render.yaml"
