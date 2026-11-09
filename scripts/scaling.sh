#!/usr/bin/env bash
# Application-only experiment. Never provisions/deletes infrastructure or storage.
set -euo pipefail
source "$(dirname "$0")/kind-common.sh"
assert_context
mode=${1:-}
[[ $mode == deploy || $mode == test ]] || {
  echo 'Usage: scaling.sh deploy|test' >&2
  exit 2
}
export SCALING_ROOT="$ROOT"
work=$(mktemp -d)
export SCALING_HISTORY_FILE=${SCALING_HISTORY_FILE:-$work/history.json}
export SCALING_FORWARD_DIR="$work/forwards"
mkdir "$SCALING_FORWARD_DIR"
changed=false

stop_forwards() {
  local record pid expected actual failed=0
  for record in "$SCALING_FORWARD_DIR"/*; do
    [[ -f $record ]] || continue
    pid=${record##*/}
    read -r expected <"$record"
    actual=$(ps -ww -p "$pid" -o args=) || continue
    if [[ $actual == "$expected" ]]; then
      kill "$pid" 2>/dev/null || failed=1
    else
      echo "Refusing reused/nonmatching tunnel PID $pid" >&2
      failed=1
    fi
  done
  return "$failed"
}

cleanup() {
  rc=$?
  trap - EXIT
  if [[ $mode == test && $changed == true ]]; then
    if ((rc != 0)); then
      for worker in worker-a worker-b worker-c worker-d; do
        k -n "temporal-demo-$worker" get scaledobjects -o json >&2 || true
        k -n "temporal-demo-$worker" logs -l app --tail=30 --max-log-requests=5 >&2 || true
      done
      k -n keda logs deployment/keda-operator --tail=40 >&2 || true
    fi
    restore_rc=0
    go test -tags integration -count=1 -v -timeout 90s ./tests/integration -run '^TestScalingCleanup$' || restore_rc=1
    stop_forwards || restore_rc=1
    for worker in worker-a worker-b worker-c worker-d; do
      k -n "temporal-demo-$worker" delete scaledobject \
        -l temporal-demo.io/scaling=true --wait=true --timeout=60s || restore_rc=1
    done
    for file in "$work"/snapshot-*.json; do
      k apply -f "$file" || restore_rc=1
    done
    for worker in orchestrator worker-a worker-b worker-c worker-d; do
      k -n "temporal-demo-$worker" rollout status "deployment/$worker" --timeout=180s || restore_rc=1
    done
    if ((restore_rc == 0)); then
      go test -tags integration -count=1 -v -timeout 60s ./tests/integration -run '^TestScalingPreflight$' || restore_rc=1
      k -n temporal get pvc data-postgres-0 \
        -o jsonpath='{.metadata.uid}:{.spec.volumeName}:{.status.phase}' >"$work/pvc-after" || restore_rc=1
      k -n temporal get secret postgres-credentials \
        -o jsonpath='{.metadata.uid}:{.metadata.resourceVersion}:{.immutable}' >"$work/secret-after" || restore_rc=1
      cmp "$work/pvc-before" "$work/pvc-after" || restore_rc=1
      cmp "$work/secret-before" "$work/secret-after" || restore_rc=1
      bash scripts/kind-integration.sh baseline || restore_rc=1
    fi
    if ((restore_rc != 0)); then
      echo "Static baseline restore FAILED; snapshots retained at $work" >&2
      exit 1
    fi
    echo 'PASS: static baseline restored and verified; retained storage/history untouched'
  fi
  stop_forwards || rc=1
  rm -rf "$work"
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 143' TERM
trap 'exit 130' INT

# Read-only, bounded checks precede every application mutation (including restore).
go test -tags integration -count=1 -v -timeout 60s ./tests/integration -run '^TestScalingPreflight$'
bootstrap=true
found=0
for worker in worker-a worker-b worker-c worker-d; do
  object=$(k -n "temporal-demo-$worker" get scaledobject "$worker" --ignore-not-found -o json)
  if [[ -n $object ]]; then
    jq -e '.metadata.labels["temporal-demo.io/scaling"] == "true"' <<<"$object" >/dev/null || {
      echo "Refusing foreign ScaledObject $worker" >&2
      exit 1
    }
    found=$((found + 1))
  fi
done
if ((found == 4)); then
  bootstrap=false
elif ((found != 0)); then
  echo 'Partial scaler installation: restore static baseline before first bootstrap' >&2
  exit 1
fi

if [[ $mode == test ]]; then
  export SCALING_RUNS_FILE="$work/runs.jsonl"
  k -n temporal get pvc data-postgres-0 \
    -o jsonpath='{.metadata.uid}:{.spec.volumeName}:{.status.phase}' >"$work/pvc-before"
  k -n temporal get secret postgres-credentials \
    -o jsonpath='{.metadata.uid}:{.metadata.resourceVersion}:{.immutable}' >"$work/secret-before"
  for worker in orchestrator worker-a worker-b worker-c worker-d; do
    resources=("deployment/$worker" "service/$worker")
    if [[ $worker == orchestrator ]]; then
      resources+=(configmap/static-registry)
    fi
    k -n "temporal-demo-$worker" get "${resources[@]}" -o json \
      | jq 'del(.metadata) | .items |= map(
          del(.status, .metadata.uid, .metadata.resourceVersion,
              .metadata.generation, .metadata.creationTimestamp, .metadata.managedFields)
          | if .kind == "Deployment" then .spec.replicas=1 else . end
        )' >"$work/snapshot-$worker.json"
  done
  changed=true
  bash scripts/scaling.sh deploy
  go test -tags integration -count=1 -v -timeout 25m ./tests/integration -run "${SCALING_TEST_RUN:-.}"
  exit 0
fi

image=$(k -n temporal-demo-orchestrator get deployment orchestrator \
  -o jsonpath='{.spec.template.spec.containers[0].image}')
tag=${image##*:}
helm template temporal-demo k8s/worker-helm-chart \
  -f k8s/worker-helm-chart/values-static.yaml \
  -f k8s/worker-helm-chart/values-scaling.yaml \
  --set-string "images.tag=$tag" --set "scaling.bootstrapZero=$bootstrap" \
  --kube-version 1.35.8 >"$work/apps.yaml"
go run ./cmd/validate-graph <"$work/apps.yaml"
bash scripts/validate-k8s.sh "$work/apps.yaml"
k apply -f "$work/apps.yaml"

# Explicit first bootstrap is one-shot. Remove replicas from the client-side
# last-applied configuration WITHOUT writing spec before the steady-state apply.
# Otherwise kubectl's three-way merge deletes replicas and defaults it to one.
helm template temporal-demo k8s/worker-helm-chart \
  -f k8s/worker-helm-chart/values-static.yaml \
  -f k8s/worker-helm-chart/values-scaling.yaml \
  --set-string "images.tag=$tag" --kube-version 1.35.8 >"$work/steady.yaml"
go run ./cmd/validate-graph <"$work/steady.yaml"
k apply set-last-applied -f "$work/steady.yaml" --create-annotation=true
bash scripts/validate-k8s.sh "$work/steady.yaml"
k apply -f "$work/steady.yaml"
echo "PASS: native scaling applied (bootstrapZero=$bootstrap); static registry retained"
