#!/usr/bin/env bash
set -euo pipefail
if (($# != 1)) || [[ $1 != keda && $1 != kyverno ]]; then
  echo 'Usage: render-controller.sh keda|kyverno' >&2
  exit 1
fi
controller=$1
source k8s/infrastructure/versions.env

case $controller in
  keda)
    version=$KEDA_CHART_VERSION
    sha256=$KEDA_CHART_SHA256
    url="https://kedacore.github.io/charts/keda-$version.tgz"
    ;;
  kyverno)
    version=$KYVERNO_CHART_VERSION
    sha256=$KYVERNO_CHART_SHA256
    url="https://kyverno.github.io/kyverno/kyverno-$version.tgz"
    ;;
esac

cache=.local/charts
archive="$cache/$controller-$version.tgz"
mkdir -p "$cache"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

verify() {
  local actual
  actual=$(openssl dgst -sha256 "$1")
  [[ ${actual##* } == "$sha256" ]] || {
    echo "$controller archive SHA256 mismatch; refusing render" >&2
    return 1
  }
}

if [[ ! -f $archive ]]; then
  curl --fail --location --silent --show-error \
    --connect-timeout 15 --max-time 120 --retry 2 --retry-max-time 240 \
    "$url" -o "$work/chart.tgz"
  verify "$work/chart.tgz"
  mv "$work/chart.tgz" "$archive"
fi

# Cached bytes are never trusted implicitly or silently replaced on mismatch.
verify "$archive"
helm template "$controller" "$archive" --namespace "$controller" --kube-version 1.35.8 \
  --include-crds --no-hooks --skip-tests -f "k8s/infrastructure/$controller-values.yaml" >"$work/render.yaml"

# Helm does not emit the release namespace. Emit only after a successful render.
printf 'apiVersion: v1\nkind: Namespace\nmetadata:\n  name: %s\n  labels:\n    temporal-demo.io/phase: namespace\n---\n' "$controller"
cat "$work/render.yaml"
