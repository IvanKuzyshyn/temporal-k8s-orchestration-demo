# Scale-to-zero

KEDA is part of every supported installation; no baseline always-running worker
mode or optional running-workflow-count signal remains. The orchestrator stays at
one replica so it can accept HTTP requests and poll its parent workflow queue.
Each worker Deployment is managed by a native Temporal ScaledObject and scales 0–1.

## Queue configuration

Each ScaledObject targets its same-named worker Deployment and queue
`<worker-name>.v1.temporal` in Temporal namespace `default`. It queries
`temporal.temporal.svc.cluster.local:7233` for both workflow and activity queue
backlog. Settings:

- Polling interval: 5 seconds.
- Activation backlog threshold: 0; target queue size: 1.
- Min replicas: 0; max replicas: 1.
- Cooldown: 120 seconds.
- `includeRunningWorkflowCount: "false"`.
- TLS disabled inside this local-only demo cluster.

A registered idle worker has no Pod. An arriving child workflow task creates
backlog, KEDA activates its worker and Kubernetes starts the Pod. Activity tasks
use the same worker queue. UI poller records can remain cached after Pods exit;
inspect actual Deployment/Pod state to establish zero capacity.

## Replica ownership

Initial bootstrap explicitly uses zero worker replicas, preventing idle Pods
before KEDA takes control. The steady-state chart omits `spec.replicas` entirely.
Before steady-state apply, bootstrap removes the old replica field from
kubectl's last-applied annotation, including interrupted-bootstrap retries.
Repeated `up` skips installation entirely for initialized state.

This split is intentional: always rendering zero would fight the autoscaler, and
omitting replicas on first creation would default workers to one.

## Work and shutdown

Demo activities take five seconds, retry and heartbeat. Workers have bounded SDK
shutdown and Pod termination grace. Queue backlog is not a production-grade count
of all in-flight work; this demo's short activities and cooldown are deliberate.
No custom scaler, backlog side channel or fake queue seeding is used.

`down` stops the node, interrupts active tasks and can let deadlines expire. It is
not a coordinated workflow pause. Real lifecycle integration proves registered
zero → backlog activation → successful diamond → eventual zero, then repeats work
after resume. See [testing](testing.md).

## Inspect

```sh
mise exec -- kubectl --kubeconfig "$PWD/.local/kubeconfig" \
  --context kind-temporal-demo get scaledobjects,hpa -A
mise exec -- kubectl --kubeconfig "$PWD/.local/kubeconfig" \
  --context kind-temporal-demo get deployments,pods -A
curl -sS http://127.0.0.1:18080/graph | jq
```

A broken graph prevents new submissions; a broken scaler may leave queued work
without Pods. Inspect events and the KEDA operator logs without printing Secrets.
