#!/usr/bin/env bash
# Combined application-only acceptance. Retained infrastructure is never reset.
set -euo pipefail
source "$(dirname "$0")/kind-common.sh"
assert_context
mode=${1:-smoke}
[[ $mode == smoke || $mode == resilience ]] || exit 2
umask 077
proof=$(mktemp -d "$ROOT/.local/task10-${mode}-XXXXXXXX")
export ACCEPTANCE_PROOF="$proof" SCALING_ROOT="$ROOT" DEMO_KUBE_CONTEXT="$CONTEXT"
export SCALING_HISTORY_FILE="$proof/retained-history.json" SCALING_RUNS_FILE="$proof/runs.jsonl"
export SCALING_FORWARD_DIR="$proof/forwards"
mkdir "$SCALING_FORWARD_DIR"
changed=false
pids=()
echo "Task10 proof: $proof"

identity() {
  local name
  for name in orchestrator worker-a worker-b worker-c worker-d; do
    k -n "temporal-demo-$name" get service "$name" -o json | jq -S '{uid:.metadata.uid,spec,annotations:.metadata.annotations}' || return 1
  done
  k -n temporal get service temporal -o json | jq -S '{uid:.metadata.uid,spec,annotations:.metadata.annotations}' || return 1
  k -n temporal get pvc data-postgres-0 -o json | jq -S '{uid:.metadata.uid,volume:.spec.volumeName,phase:.status.phase}' || return 1
  k -n temporal get secret postgres-credentials -o jsonpath='{.metadata.uid}:{.metadata.resourceVersion}:{.immutable}' || return 1
  printf '\n'
  k -n temporal get pods -l app=temporal -o json | jq -S '[.items[].metadata.uid]' || return 1
  k get clusterpolicy temporal-validate-workers temporal-generate-registrations -o json | jq -S '[.items[] | {uid:.metadata.uid,spec}]' || return 1
}

stop_forwards() {
  local pid record expected actual failed=0
  for pid in ${pids[@]+"${pids[@]}"}; do
    if kill -0 "$pid" 2>/dev/null; then kill "$pid" || failed=1; fi
    wait "$pid" 2>/dev/null || true
  done
  for record in "$SCALING_FORWARD_DIR"/*; do
    [[ -f $record ]] || continue
    pid=${record##*/}
    read -r expected <"$record"
    actual=$(ps -ww -p "$pid" -o args=) || continue
    if [[ $actual == "$expected" ]]; then
      kill "$pid" || failed=1
    else
      echo "Refusing reused tunnel PID $pid" >&2
      failed=1
    fi
  done
  return "$failed"
}

delete_created() {
  local phase=$1 kind ns name uid current failed=0
  local -a args
  # Exact UID checks accompany every deletion; no adoption by expected name.
  if [[ -f $proof/created.tsv ]]; then
    while read -r kind ns name uid; do
      if [[ $phase == scalers && $kind != ScaledObject || $phase == access && $kind == ScaledObject ]]; then continue; fi
      args=("$kind" "$name")
      case "$kind" in
        ClusterRole|ClusterRoleBinding) ;;
        *) args+=(-n "$ns") ;;
      esac
      current=$(k get "${args[@]}" --ignore-not-found -o jsonpath='{.metadata.uid}') || { failed=1; continue; }
      [[ -n $current ]] || continue
      if [[ $current != "$uid" ]]; then
        echo "Refusing changed UID: $kind $ns/$name" >&2
        failed=1
        continue
      fi
      k delete "${args[@]}" --wait=true --timeout=60s || failed=1
    done <"$proof/created.tsv"
  fi
  return "$failed"
}

restore() {
  local name uid current file annotation failed=0
  go test -tags=integration -count=1 -run '^TestScalingCleanup$' -v -timeout=90s ./tests/integration || failed=1
  delete_created scalers || failed=1
  for name in worker-a worker-b worker-c worker-d; do
    current=$(k -n "temporal-demo-$name" get service "$name" -o jsonpath='{.metadata.uid}') || { failed=1; continue; }
    uid=$(jq -r '.metadata.uid' "$proof/service-$name.json")
    if [[ $current != "$uid" ]]; then failed=1; continue; fi
    annotation=$(jq -r '.metadata.annotations | if has("temporal-worker-run-after") then "temporal-worker-run-after=" + .["temporal-worker-run-after"] else "temporal-worker-run-after-" end' "$proof/service-$name.json")
    k -n "temporal-demo-$name" annotate service "$name" "$annotation" --overwrite || failed=1
  done
  for name in orchestrator worker-a worker-b worker-c worker-d; do
    file="$proof/deployment-$name.json"
    # Restore the entire changed spec and ownership metadata, including the
    # original last-applied annotation and rollout restart annotation presence.
    jq '[{op:"test",path:"/metadata/uid",value:.metadata.uid},
      {op:"replace",path:"/spec",value:.spec},
      {op:"add",path:"/metadata/annotations",value:(.metadata.annotations // {})},
      {op:"add",path:"/metadata/labels",value:(.metadata.labels // {})}]' "$file" >"$proof/restore-$name.json"
    k -n "temporal-demo-$name" patch deployment "$name" --type=json --patch-file "$proof/restore-$name.json" || failed=1
  done
  for name in orchestrator worker-a worker-b worker-c worker-d; do
    k -n "temporal-demo-$name" rollout status "deployment/$name" --timeout=180s || failed=1
    k -n "temporal-demo-$name" get deployment "$name" -o json | jq -S '{uid:.metadata.uid,spec,annotations:(.metadata.annotations | del(.["deployment.kubernetes.io/revision"])),labels:.metadata.labels}' >"$proof/app-after-$name.json" || failed=1
    jq -S '{uid:.metadata.uid,spec,annotations:(.metadata.annotations | del(.["deployment.kubernetes.io/revision"])),labels:.metadata.labels}' "$proof/deployment-$name.json" >"$proof/app-before-$name.json"
    cmp "$proof/app-before-$name.json" "$proof/app-after-$name.json" || failed=1
  done
  # Original templates may reference the now-absent discovery ServiceAccount.
  # Keep it until the restored Pods are Ready, then restore original absence.
  if ((failed == 0)); then delete_created access || failed=1; fi
  k wait --for=condition=Ready clusterpolicy/temporal-validate-workers clusterpolicy/temporal-generate-registrations --timeout=60s || failed=1
  identity >"$proof/identity-after" || failed=1
  cmp "$proof/identity-before" "$proof/identity-after" || failed=1
  go test -tags=integration -count=1 -run '^TestScalingPreflight$' -v -timeout=60s ./tests/integration || failed=1
  return "$failed"
}

cleanup() {
  rc=$?
  trap - EXIT
  if ((rc != 0)); then
    bash scripts/collect-diagnostics.sh "$proof" || rc=1
  fi
  if [[ $changed == true ]]; then
    restore >"$proof/restore.log" 2>&1 || rc=1
    tail -12 "$proof/restore.log"
  fi
  stop_forwards || rc=1
  printf '%s\n' "$rc" >"$proof/exit"
  echo "Task10 $mode exit=$rc; proof retained: $proof"
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 143' TERM
trap 'exit 130' INT

forward() {
  local ns=$1 service=$2 remote=$3 port='' pid i
  kubectl --kubeconfig "$KUBECONFIG" --context "$CONTEXT" -n "$ns" port-forward \
    --address 127.0.0.1 "service/$service" ":$remote" >"$proof/forward-$service.log" 2>&1 &
  pid=$!
  pids+=("$pid")
  for ((i = 0; i < 75; i++)); do
    kill -0 "$pid" 2>/dev/null || return 1
    port=$(awk '/Forwarding from 127.0.0.1:/ {split($3,a,":"); print a[2]; exit}' "$proof/forward-$service.log")
    if [[ $port =~ ^[0-9]+$ ]]; then endpoint="127.0.0.1:$port"; return 0; fi
    sleep 0.2
  done
  return 1
}

# All read-only checks/snapshots precede the first mutation.
: "${TASK10_TRUSTED_UID:?set the explicitly verified owned kube-system UID before acceptance}"
[[ $(k get namespace kube-system -o jsonpath='{.metadata.uid}') == "$TASK10_TRUSTED_UID" ]] || {
  echo 'Unapproved cluster UID (disposable CI must explicitly trust its freshly created UID)' >&2
  exit 1
}
[[ $(docker inspect temporal-demo-control-plane --format '{{index .Config.Labels "io.x-k8s.kind.cluster"}}') == temporal-demo ]]
for name in orchestrator worker-a worker-b worker-c worker-d; do
  k -n "temporal-demo-$name" rollout status "deployment/$name" --timeout=60s
  k -n "temporal-demo-$name" get deployment "$name" -o json >"$proof/deployment-$name.json"
  [[ $(jq -r '.spec.replicas' "$proof/deployment-$name.json") == 1 ]]
  k -n "temporal-demo-$name" get service "$name" -o json >"$proof/service-$name.json"
done
[[ $(jq -r '.spec.template.spec.containers[0].env[] | select(.name=="REGISTRY_MODE") | .value' "$proof/deployment-orchestrator.json") == static ]]
k wait --for=condition=Ready clusterpolicy/temporal-validate-workers clusterpolicy/temporal-generate-registrations --timeout=60s
identity >"$proof/identity-before"
go test -tags=integration -count=1 -run '^TestScalingPreflight$' -v -timeout=60s ./tests/integration | tee "$proof/preflight.log"

tag="$(git rev-parse HEAD)-task10-$(date +%s)-$(openssl rand -hex 4)"
docker build -f deploy/orchestrator.Dockerfile -t "temporal-demo/orchestrator:$tag" . >"$proof/build-orchestrator.log" 2>&1
docker build -f deploy/worker.Dockerfile -t "temporal-demo/worker:$tag" . >"$proof/build-worker.log" 2>&1
kind load docker-image --name "$CLUSTER" "temporal-demo/orchestrator:$tag" "temporal-demo/worker:$tag"
if [[ $mode == resilience ]]; then
  export TEMPORAL_RELAY_IMAGE="temporal-demo/task10-relay:$tag"
  docker build -f tests/integration/relay.Dockerfile -t "$TEMPORAL_RELAY_IMAGE" . >"$proof/build-relay.log" 2>&1
  kind load docker-image --name "$CLUSTER" "$TEMPORAL_RELAY_IMAGE"
fi
render() {
  helm template temporal-demo k8s/worker-helm-chart \
    -f k8s/worker-helm-chart/values-static.yaml \
    -f k8s/worker-helm-chart/values-discovery.yaml \
    -f k8s/worker-helm-chart/values-scaling.yaml \
    --set-string "images.tag=$tag" --set "scaling.bootstrapZero=$1" --kube-version 1.35.8
}
render true >"$proof/apps.yaml"
go run ./cmd/validate-graph <"$proof/apps.yaml"
bash scripts/validate-k8s.sh "$proof/apps.yaml"
# Do not reapply original Services/Namespaces/ConfigMaps. Discovery observes
# their retained canonical annotations; only owned apps/RBAC/scalers change.
k create --dry-run=client -f "$proof/apps.yaml" -o json | jq -s '{apiVersion:"v1",kind:"List",items:[.[] | select(.kind!="Service" and .kind!="Namespace" and .kind!="ConfigMap")]}' >"$proof/owned.json"
jq -c '.items[] | select(.kind!="Deployment")' "$proof/owned.json" >"$proof/new.jsonl"
while IFS= read -r object; do
  kind=$(jq -r '.kind' <<<"$object"); ns=$(jq -r '.metadata.namespace // "default"' <<<"$object"); name=$(jq -r '.metadata.name' <<<"$object")
  [[ -z $(k -n "$ns" get "$kind" "$name" --ignore-not-found -o name) ]] || { echo "Refusing preexisting $kind $ns/$name" >&2; exit 1; }
done <"$proof/new.jsonl"
changed=true
while IFS= read -r object; do
  k create -f - -o json <<<"$object" >"$proof/created-last.json"
  jq -r '[.kind,(.metadata.namespace // "default"),.metadata.name,.metadata.uid] | @tsv' "$proof/created-last.json" >>"$proof/created.tsv"
done <"$proof/new.jsonl"
jq '.items |= map(select(.kind=="Deployment"))' "$proof/owned.json" >"$proof/deployments.json"
k apply -f "$proof/deployments.json"
render false >"$proof/steady.yaml"
k create --dry-run=client -f "$proof/steady.yaml" -o json | jq -s '{apiVersion:"v1",kind:"List",items:[.[] | select(.kind=="Deployment")]}' >"$proof/steady.json"
k apply set-last-applied -f "$proof/steady.json" --create-annotation=true
k apply -f "$proof/steady.json"
k -n temporal-demo-orchestrator rollout status deployment/orchestrator --timeout=180s
forward temporal temporal 7233
export TEMPORAL_ADDRESS="$endpoint"
forward temporal-demo-orchestrator orchestrator 8080
export DEMO_API_URL="http://$endpoint"
if [[ $mode == smoke ]]; then
  go test -tags=integration,acceptance_integration -count=1 -run '^TestDiamondFromZero$' -v -timeout=10m ./tests/integration | tee "$proof/tests.log"
else
  go test -tags=integration,resilience_integration -count=1 -run '^Test(PlanSnapshotIsolation|Recovery)' -v -timeout=30m ./tests/integration | tee "$proof/tests.log"
fi
