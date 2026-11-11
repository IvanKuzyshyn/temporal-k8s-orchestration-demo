# Isolated native Temporal → KEDA scaling

This experiment keeps **static discovery** enabled. It does not install Kyverno
registration policies or enable Kubernetes discovery. The orchestrator remains
at one replica; each worker has a same-namespace ScaledObject targeting its
Deployment. Only KEDA creates HPAs; our chart creates none.

## Run on the existing dedicated kind cluster

```sh
mise run apps:deploy-scaling
SCALING_SKIP_LONG=0 mise run test:scaling
```

Use this worktree's existing `.local/kubeconfig`, context
`kind-temporal-demo`. The tasks refuse a foreign `KUBECONFIG` and check for
running Temporal executions before application fixture changes. Schedule this
experiment exclusively: visibility is eventually consistent, not an execution
lock. Do not use cluster deletion, database reset, or credential replacement to
prepare an experiment.

`apps:deploy-scaling` reuses the installed image tag. On first installation
(all four owned ScaledObjects absent), it explicitly renders
`scaling.bootstrapZero=true`. This is the one-time zero-replica bootstrap.
Subsequent applies omit worker replicas. Partial/foreign scaler installations
are rejected rather than silently adopted.

After bootstrap, native `kubectl apply set-last-applied` updates the recorded
configuration to the replica-free steady-state artifact without writing the
Deployment spec. Otherwise a later client-side three-way merge can delete
`replicas`, defaulting it to one. The steady-state artifact is graph-checked,
strictly server-dry-run, and applied unchanged. Reapply testing watches actual
replica values, including transient changes; Deployment generation alone is not
an ownership test (Kubernetes also increments it for annotation updates).

`test:scaling` works from either the fixed or scaled baseline. It snapshots the
actual demo Deployments, Services and static registry, then installs fixture
values. On exit, including assertion failure, it terminates exact owned test
executions/children, removes only owned app ScaledObjects, restores fixed
replicas/configuration, waits for readiness, and runs `test:kind-baseline`.
Its temporary exact execution-ID list also permits SDK cleanup after a test
process timeout. Localhost tunnels belong to individual tests and are closed
on test exit. No infrastructure/PVC/Secret/history teardown occurs. PVC and
credential-identity metadata and a pre-existing completed history hash are
compared across the experiment. A failed restoration retains its snapshot path
and fails the task; do not treat that as successful cleanup.

## Pinned native contract

KEDA chart/app **2.21.0**, Temporal server **1.32.0**, Kubernetes **1.35.8**;
see [versions and host evidence](versions.md). No speculative repinning or
additional scaler service is needed.

The overlay uses polling **5s**, cooldown **120s**, min/max **0/1**,
`activationTargetQueueSize: "0"`, `targetQueueSize: "1"`, and
`queueTypes: "workflow,activity"`. Endpoint:
`temporal.temporal.svc.cluster.local:7233`, namespace `default`. The local
server is plaintext, so `enableTLS: "false"` is explicit. The metadata default
is true, but this release's no-auth client path constructs no TLS config
regardless; API-key-only connections honor the flag. Queue names remain
`<service-name>.v1.temporal`.

[Tagged KEDA implementation](https://github.com/kedacore/keda/blob/v2.21.0/pkg/scalers/temporal_scaler.go)
uses ordinary `DescribeTaskQueue` with `ReportStats=true` per queue type for
unversioned workers, summing `Stats.ApproximateBacklogCount`. It does not infer
backlog from poller counts or require version-selection metadata. Runtime tests
must prove the selected server actually returns those stats with no live worker
Pods. Cached Temporal poller entries can outlive a Pod; they are not live-worker
evidence.

For a worker, inspect conditions separately from inactivity:

```sh
kubectl --kubeconfig .local/kubeconfig --context kind-temporal-demo \
  -n temporal-demo-worker-a get scaledobject worker-a -o json
kubectl --kubeconfig .local/kubeconfig --context kind-temporal-demo \
  -n keda logs deployment/keda-operator --tail=50
```

`Ready=True, Active=False/ScalerNotActive` is normal for an empty queue.
`Ready=False`, failures in scaler health, or operator RPC errors are not an
empty queue. Status updates can lag a replica transition. Tests retain bounded
condition/error evidence rather than interpreting every inactive scaler as
healthy. A controlled unreachable-endpoint test must surface an error condition,
then recover to Ready/inactive after the endpoint is restored.

## What the experiments mean

- **Activation:** a singleton registered graph first, then the canonical diamond.
  Both use the real zero-body HTTP API/static registry, `plan.Build`, production
  parent/child workflows and identity-only results. Workers begin/end at zero
  real Pods. Production polling/cooldown defaults are retained.
- **In-flight shutdown:** test-only cooldown **5s**, configured activity **50s**,
  unchanged worker stop **30s** / Kubernetes grace **45s**. An SDK-hosted
  test-only workflow supplies start-to-close **2min**, heartbeat **15s**, retry
  delay **90s**, max attempts **3**, schedule-to-close **10min**. It sends only
  the existing worker identity to the existing activity. Actual Pod shutdown
  logs and pending-attempt/history evidence distinguish an interruption from
  successful graceful completion. Recovery switches the fixture back to a
  **120s** cooldown and **5s** activity; it asserts bounded completion, not
  uninterrupted execution. Production contracts/timeouts are not broadened.
- **Delayed retry:** test cooldown **20s**; deliberately force-delete the owned
  application's first 50s activity Pod. Observe its heartbeat timeout and the
  server's next-attempt timestamp, then prove real Pods reached zero before
  that timestamp. Switch the fixture activity to 5s. Temporarily pause KEDA at
  zero to observe the delayed retry's **activity backlog >0, workflow backlog
  =0**, using the exact native stats RPC. Unpause, observe activation and
  successful attempt ≥2, then return to zero. The host workflow poller is on a
  different queue; there are no live app-worker pollers when backlog is proven.
  Pausing is a measurement barrier, not a production scaler setting.
- **Optional running count:** `scaling.includeRunningWorkflowCount=true` is
  opt-in, default **false**. Pinned KEDA supports it and queries visibility
  with `ExecutionStatus = 'Running' AND TaskQueue = '<queue>' AND
  TemporalWorkerDeploymentVersion is null` for unversioned workers. The test
  exercises that query and the scaler with a 50s production child activity and
  5s test cooldown. Visibility is approximate/eventually consistent; the flag
  is **not an in-flight safety guarantee**. In this release visibility-count
  errors are treated as no signal, unlike backlog RPC errors.

Automatic kind CI runs activation/completion/zero, handoff, delayed retry and
optional-count checks using the same task; manual dispatch additionally runs
long in-flight shutdown (`SCALING_SKIP_LONG=1` skips only that experiment in
PR CI). Required local acceptance includes the long experiment. For focused development,
`SCALING_TEST_RUN=TestInFlightScaleDown mise run test:scaling` still restores and
verifies the baseline; omit the filter for full acceptance. Latest-head
Linux results are controller-observed, not inferred from local ARM64 tests.
Historical Task10 PR CI passed the bounded selection but skipped in-flight
shutdown; current Task11 local full scaling (including that case) passed in
816 seconds. Exact results and provenance are in [experiments](experiments.md).

After both isolated scaling and discovery/RBAC pass and restore their baseline,
`mise run smoke` temporarily combines native scaling with live discovery. It
requires explicit verified `TASK10_TRUSTED_UID` and a completed-history witness;
it restores fixed/static mode on exit rather than leaving a combined deployment.
Do not run `k8s:up` or another experiment while KEDA owns live replicas. Five-second
polling and 120-second cooldown are not promises of five-second Pod readiness
or guaranteed uninterrupted work.
