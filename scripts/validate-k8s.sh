#!/usr/bin/env bash
# Internal validation of the exact render that bootstrap will apply.
set -euo pipefail
source "$(dirname "$0")/kind-common.sh"
assert_context
[[ $# == 1 && -f $1 ]] || { echo 'Usage: validate-k8s.sh rendered-manifest.yaml' >&2; exit 2; }
k apply --dry-run=server --validate=strict -f "$1"
