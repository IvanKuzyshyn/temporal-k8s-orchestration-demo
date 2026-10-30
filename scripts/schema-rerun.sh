#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/kind-common.sh"
assert_context
k -n temporal delete job temporal-schema --ignore-not-found --wait=true --timeout=60s
k apply -f k8s/infrastructure/schema-job.yaml
k -n temporal wait --for=condition=complete job/temporal-schema --timeout=270s
echo 'PASS: matching default/visibility schema Job completed safely'
