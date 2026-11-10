#!/usr/bin/env bash
# Explicit application-only discovery phase. No cluster/controller/storage lifecycle.
set -euo pipefail
source "$(dirname "$0")/kind-common.sh"
assert_context
mode=${1:-}
case "$mode" in
  deploy|test|rbac|restore)
    ;;
  *)
    echo 'Usage: discovery.sh deploy|test|rbac|restore' >&2
    exit 2
    ;;
esac
proof="$ROOT/.local/task9"
baseline="$proof/static-baseline"
mkdir -p "$proof"
export REGISTRY_ROOT="$ROOT" SCALING_ROOT="$ROOT"
export SCALING_HISTORY_FILE="$proof/retained-history.json"
export SCALING_RUNS_FILE="$proof/runs.jsonl"
export REGISTRY_NAMESPACES_FILE="$proof/namespaces"
export SCALING_FORWARD_DIR="$proof/forwards"
mkdir -p "$SCALING_FORWARD_DIR"
changed=false
snapshot_created=false

stop_forwards() {
  local record pid expected actual failed=0
  for record in "$SCALING_FORWARD_DIR"/*; do
    [[ -f $record ]] || continue
    pid=${record##*/}
    read -r expected <"$record"
    actual=$(ps -ww -p "$pid" -o args=) || continue
    if [[ $actual == "$expected" ]]; then
      kill "$pid" 2>/dev/null || failed=1
      rm -f "$record"
    else
      echo "Refusing reused/nonmatching tunnel PID $pid" >&2
      failed=1
    fi
  done
  return "$failed"
}

identity() {
  local name
  for name in orchestrator worker-a worker-b worker-c worker-d; do
    printf '%s=' "$name"
    k -n "temporal-demo-$name" get service "$name" -o jsonpath='{.metadata.uid}' || return 1
    printf '\n'
  done
  k -n temporal get pvc data-postgres-0 -o jsonpath='{.metadata.uid}:{.spec.volumeName}:{.status.phase}' || return 1
  printf '\n'
  k -n temporal get secret postgres-credentials -o jsonpath='{.metadata.uid}:{.metadata.resourceVersion}:{.immutable}' || return 1
  printf '\n'
}
policies_ready() {
  k wait --for=condition=Ready clusterpolicy/temporal-validate-workers \
    clusterpolicy/temporal-generate-registrations --timeout=60s
}
restore() {
  [[ -f $baseline/identity ]] || {
    echo 'No Task9 static snapshot; refusing broad restoration' >&2
    return 1
  }
  local name annotation failed=0
  # Repeat cleanup only for exact execution IDs recorded by this experiment.
  go test -tags integration -count=1 -run '^TestScalingCleanup$' -v -timeout=90s ./tests/integration || failed=1
  for name in worker-a worker-b worker-c worker-d; do
    annotation=$(jq -r '.metadata.annotations | if has("temporal-worker-run-after") then "temporal-worker-run-after=" + .["temporal-worker-run-after"] else "temporal-worker-run-after-" end' "$baseline/service-$name.json")
    k -n "temporal-demo-$name" annotate service "$name" "$annotation" --overwrite || failed=1
  done
  if [[ -f $REGISTRY_NAMESPACES_FILE ]]; then
    while IFS= read -r name; do
      if [[ $name != registry-* ]]; then
        failed=1
        continue
      fi
      k delete namespace "$name" --ignore-not-found --wait=true --timeout=90s || failed=1
    done <"$REGISTRY_NAMESPACES_FILE"
  fi
  for name in orchestrator worker-a worker-b worker-c worker-d; do
    k apply -f "$baseline/$name.json" || failed=1
  done
  for name in orchestrator worker-a worker-b worker-c worker-d; do
    k -n "temporal-demo-$name" rollout status "deployment/$name" --timeout=180s || failed=1
  done
  # These names were checked absent before the experiment's first mutation.
  k delete clusterrolebinding,clusterrole temporal-demo-registry --ignore-not-found || failed=1
  for name in worker-a worker-b worker-c worker-d; do
    k -n "temporal-demo-$name" delete rolebinding,role temporal-demo-registry --ignore-not-found || failed=1
  done
  k -n temporal-demo-orchestrator delete serviceaccount orchestrator --ignore-not-found || failed=1
  policies_ready || failed=1
  identity >"$proof/identity-after" || failed=1
  cmp "$baseline/identity" "$proof/identity-after" || failed=1
  go test -tags integration -count=1 -run '^TestScalingPreflight$' -v -timeout=60s ./tests/integration || failed=1
  if ((failed == 0)); then
    rm -rf "$baseline"
    echo 'PASS: static baseline restored; five fixed apps/policies Ready; Service/PVC/Secret/history preserved'
  fi
  return "$failed"
}
cleanup() {
  rc=$?
  trap - EXIT
  if ((rc != 0)); then
    if [[ $changed == true ]]; then
      restore || rc=1
    elif [[ $snapshot_created == true ]]; then
      rm -rf "$baseline"
    fi
  fi
  stop_forwards || rc=1
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 143' TERM
trap 'exit 130' INT

if [[ $mode == restore ]]; then
  restore
  exit 0
fi
if [[ $mode == test || $mode == rbac ]]; then
  # Do not restore anything on a preflight failure.
  go test -tags integration -count=1 -run '^TestScalingPreflight$' -v -timeout=60s ./tests/integration
  [[ -f $baseline/identity ]] || {
    echo 'Run apps:deploy-discovery before discovery tests' >&2
    exit 1
  }
  changed=true
  selector='^TestLiveRegistry$'
  if [[ $mode == rbac ]]; then
    selector='^TestRegistryRBAC$'
  fi
  go test -tags=integration,registry_integration -count=1 -run "$selector" -v -timeout=20m ./tests/integration
  exit 0
fi

# Read-only checks and snapshots precede any deployment/RBAC mutation.
go test -tags integration -count=1 -run '^TestScalingPreflight$' -v -timeout=60s ./tests/integration
policies_ready
[[ ! -e $baseline ]] || {
  echo 'Existing Task9 snapshot: restore before a new deployment' >&2
  exit 1
}
for name in worker-a worker-b worker-c worker-d; do
  objects=$(k -n "temporal-demo-$name" get scaledobjects --ignore-not-found -o name)
  [[ -z $objects ]] || {
    echo 'Discovery phase requires restored fixed workers, not active KEDA ownership' >&2
    exit 1
  }
  objects=$(k -n "temporal-demo-$name" get role,rolebinding temporal-demo-registry --ignore-not-found -o name)
  [[ -z $objects ]]
done
objects=$(k get clusterrole,clusterrolebinding temporal-demo-registry --ignore-not-found -o name)
[[ -z $objects ]]
objects=$(k -n temporal-demo-orchestrator get serviceaccount orchestrator --ignore-not-found -o name)
[[ -z $objects ]]
registry_mode=$(k -n temporal-demo-orchestrator get deployment orchestrator \
  -o 'jsonpath={.spec.template.spec.containers[0].env[?(@.name=="REGISTRY_MODE")].value}')
[[ $registry_mode == static ]]
mkdir -m 700 "$baseline"
snapshot_created=true
rm -f "$SCALING_RUNS_FILE" "$REGISTRY_NAMESPACES_FILE"
identity >"$baseline/identity"
for name in orchestrator worker-a worker-b worker-c worker-d; do
  k -n "temporal-demo-$name" get deployment "$name" -o json \
    | jq 'del(.status, .metadata.uid, .metadata.resourceVersion, .metadata.generation,
        .metadata.creationTimestamp, .metadata.managedFields)' >"$baseline/$name.json"
done
for name in worker-a worker-b worker-c worker-d; do
  k -n "temporal-demo-$name" get service "$name" -o json >"$baseline/service-$name.json"
done

# Build the new consumer, without changing shared image/tool/controller pins.
tag="$(git rev-parse HEAD)-task9-$(date +%s)-$(openssl rand -hex 4)"
docker build -f deploy/orchestrator.Dockerfile -t "temporal-demo/orchestrator:$tag" .
docker build -f deploy/worker.Dockerfile -t "temporal-demo/worker:$tag" .
kind load docker-image --name "$CLUSTER" "temporal-demo/orchestrator:$tag" "temporal-demo/worker:$tag"
helm template temporal-demo k8s/worker-helm-chart \
  -f k8s/worker-helm-chart/values-static.yaml \
  -f k8s/worker-helm-chart/values-discovery.yaml \
  --set-string "images.tag=$tag" --kube-version 1.35.8 >"$proof/apps.yaml"
go run ./cmd/validate-graph <"$proof/apps.yaml"
bash scripts/validate-k8s.sh "$proof/apps.yaml"
changed=true
k apply -f "$proof/apps.yaml"
for name in orchestrator worker-a worker-b worker-c worker-d; do
  k -n "temporal-demo-$name" rollout status "deployment/$name" --timeout=180s
done
policies_ready
identity >"$proof/identity-discovery"
cmp "$baseline/identity" "$proof/identity-discovery"
echo 'PASS: Kubernetes registry deployed after policy readiness; fixed workers; static rollback retained'
