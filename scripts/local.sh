#!/usr/bin/env bash
# Persistent full demo lifecycle. Never adopts an existing cluster implicitly.
set -euo pipefail
source "$(dirname "$0")/kind-common.sh"
source k8s/infrastructure/versions.env
mode=${1:-}
[[ $mode == up || $mode == down || $mode == destroy ]] || {
  echo 'Usage: local.sh up|down|destroy' >&2; exit 2;
}
umask 077
state="$ROOT/.local/lifecycle"
mkdir -p "$state"
lock="$ROOT/.local/lifecycle.lock"
mkdir "$lock" 2>/dev/null || {
  echo "Another lifecycle command is active. If interrupted, verify no command is running before removing $lock" >&2
  exit 1
}
trap 'rmdir "$lock"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
node=temporal-demo-control-plane

# Docker ps failure is not evidence of absence (e.g. Docker Desktop is stopped).
nodes=$(docker ps -a --filter "name=^/${node}$" --format '{{.Names}}')
present=false
[[ $nodes != "$node" ]] || present=true
verify_node() {
  [[ -s $state/node-id ]] || {
    echo 'Refusing existing cluster without this checkout ownership record. Use its original checkout, or deliberately remove the old cluster before up.' >&2
    return 1
  }
  local info
  info=$(docker inspect "$node")
  jq -e --arg id "$(<"$state/node-id")" \
    'length == 1 and .[0].Id == $id and .[0].Config.Labels["io.x-k8s.kind.cluster"] == "temporal-demo"' <<<"$info" >/dev/null || {
    echo 'Refusing foreign or replaced demo node' >&2; return 1;
  }
  node_running=$(jq -r '.[0].State.Running' <<<"$info")
}

# Store both process start time and exact command line to avoid killing reused PIDs.
process_identity() { ps -ww -p "$1" -o lstart= -o args=; }
stop_forward() {
  local name=$1 pid expected actual
  [[ -f $state/$name.pid ]] || return 0
  read -r pid <"$state/$name.pid"
  [[ $pid =~ ^[0-9]+$ ]] || { echo "Invalid $name tunnel PID" >&2; return 1; }
  expected=$(<"$state/$name.identity")
  if actual=$(process_identity "$pid"); then
    [[ $actual == "$expected" ]] || {
      echo "Refusing reused/nonmatching $name tunnel PID $pid" >&2; return 1;
    }
    kill "$pid"
    # Ensure a new tunnel cannot race the old socket closing.
    for ((i=0; i<50; i++)); do
      kill -0 "$pid" 2>/dev/null || break
      sleep 0.1
    done
  fi
  rm -f "$state/$name.pid" "$state/$name.identity"
}
stop_forwards() { stop_forward api; stop_forward ui; }
start_forward() {
  local name=$1 namespace=$2 service=$3 port=$4 pid
  [[ $port =~ ^[0-9]+$ && ${#port} -le 5 ]] && ((10#$port > 0 && 10#$port < 65536)) || {
    echo "Invalid $name port: $port" >&2; return 1;
  }
  nohup kubectl --kubeconfig "$KUBECONFIG" --context "$CONTEXT" \
    -n "$namespace" port-forward --address 127.0.0.1 "service/$service" "$port:8080" \
    >"$state/$name.log" 2>&1 </dev/null &
  pid=$!
  printf '%s\n' "$pid" >"$state/$name.pid"
  process_identity "$pid" >"$state/$name.identity"
  for ((i=0; i<100; i++)); do
    kill -0 "$pid" 2>/dev/null || break
    if grep -q 'Forwarding from 127.0.0.1:' "$state/$name.log"; then
      # nohup has now exec'd kubectl; its transient command is not an identity.
      process_identity "$pid" >"$state/$name.identity"
      return 0
    fi
    sleep 0.2
  done
  echo "$name port-forward failed; see $state/$name.log (check for occupied ports)" >&2
  return 1
}

if [[ $present == true ]]; then verify_node; fi
if [[ $mode != up ]]; then
  stop_forwards
  if [[ $mode == down ]]; then
    if [[ $present == true && $node_running == true ]]; then docker stop "$(<"$state/node-id")"; fi
    echo 'Demo stopped; cluster, credentials and workflow history retained. Resume with mise run up.'
  else
    if [[ $present == true ]]; then
      kind delete cluster --name "$CLUSTER" --kubeconfig "$KUBECONFIG"
    fi
    rm -rf "$state"
    rm -f "$KUBECONFIG" "$ROOT/.local/kind-cluster-uid"
    echo 'Demo destroyed, including local PostgreSQL history. Shared image caches left intact.'
  fi
  exit 0
fi

if [[ $present == false ]]; then
  [[ ! -f $state/node-id ]] || {
    echo 'Owned node is missing. Run mise run destroy to clear stale lifecycle state before recreating.' >&2; exit 1;
  }
  # Never claim an existing container after a failed create (another checkout
  # may have won the name race). Partial kind creation requires explicit cleanup.
  kind create cluster --name "$CLUSTER" --image "$KIND_NODE_IMAGE" \
    --config k8s/infrastructure/kind.yaml --kubeconfig "$KUBECONFIG" --wait 180s
  docker inspect "$node" | jq -er '.[0] | select(.Config.Labels["io.x-k8s.kind.cluster"] == "temporal-demo") | .Id' >"$state/node-id"
  chmod 600 "$KUBECONFIG"
  verify_node
elif [[ $node_running != true ]]; then
  docker start "$(<"$state/node-id")"
fi
assert_context
# The API may be unreachable briefly after docker start; kubectl wait alone
# does not retry an initial connection failure.
ready=false
for ((i=0; i<90; i++)); do
  if k get --raw=/readyz >/dev/null 2>&1; then ready=true; break; fi
  sleep 2
done
[[ $ready == true ]] || { echo 'Kubernetes API did not become ready' >&2; exit 1; }
k wait --for=condition=Ready "node/$node" --timeout=180s
if [[ ! -f $state/initialized ]]; then
  bash scripts/k8s-up.sh
  touch "$state/initialized"
fi
# A repeat up resumes rather than redeploying installed workloads.
k -n temporal rollout status statefulset/postgres --timeout=180s
for deployment in temporal temporal-ui; do
  k -n temporal rollout status "deployment/$deployment" --timeout=180s
done
k -n temporal-demo-orchestrator rollout status deployment/orchestrator --timeout=180s
k wait --for=condition=Ready clusterpolicy/temporal-validate-workers clusterpolicy/temporal-generate-registrations --timeout=120s
for worker in worker-a worker-b worker-c worker-d; do
  k -n "temporal-demo-$worker" wait --for=condition=Ready "scaledobject/$worker" --timeout=180s
done
stop_forwards
# On a tunnel/readiness failure close only the tunnels started by this command.
trap 'rc=$?; if ((rc != 0)); then stop_forwards || true; fi; rmdir "$lock"; exit "$rc"' EXIT
api_port=${API_FORWARD_PORT:-18080}
ui_port=${UI_FORWARD_PORT:-18088}
start_forward api temporal-demo-orchestrator orchestrator "$api_port"
start_forward ui temporal temporal-ui "$ui_port"
api="http://127.0.0.1:$api_port"
ui="http://127.0.0.1:$ui_port"
echo 'Waiting for API/UI readiness...'
ready=false
for ((i=0; i<90; i++)); do
  if curl -fs --max-time 3 "$api/readyz" >/dev/null &&
     curl -fs --max-time 3 "$ui/" >/dev/null; then ready=true; break; fi
  sleep 2
done
[[ $ready == true ]] || { echo 'API/UI readiness did not converge' >&2; exit 1; }
echo 'Waiting up to 120 seconds for the complete worker graph...'
bash scripts/wait-graph.sh "$api" "$state/expected-workers.json"
printf '\nTemporal UI: %s\nAPI:         %s\n\nTrigger a workflow:\ncurl -fsS -X POST %s/runs\n\nStop (retain data): mise run down\nDelete all demo data: mise run destroy\n' "$ui" "$api" "$api"
