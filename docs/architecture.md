# Architecture

The only supported runtime is the full local kind deployment. User operations are
`mise run up`, a body-free POST `/runs`, `mise run down`, and `mise run destroy`.

## Registration and execution

1. The chart creates per-worker Namespaces, annotated Services, Deployments and
   KEDA ScaledObjects, plus the orchestrator and scoped discovery RBAC.
2. Kyverno generates same-namespace `temporal-worker-<service>` ConfigMaps from
   selected Services. These contain worker identity, queue/type, dependencies and
   source metadata, not business data.
3. The orchestrator watches labeled Namespaces and their registration ConfigMaps.
   Every selected cache must synchronize. `plan.Build` validates unique identities,
   dependencies, cycles, queue/type contracts and canonical ordering/hash.
4. `/graph` exposes validation of the current snapshot. Startup waits for the
   complete expected deployment graph rather than accepting a valid subset.
5. A new POST `/runs` copies/validates the current graph and starts a Temporal
   parent. Existing idempotency keys resolve their durable execution first.
6. The parent starts eligible child workflows on worker-specific task queues.
   A runs first, B/C run concurrently, D waits for both. Each child runs its
   identity-only five-second activity with retries/heartbeats/cancellation.
7. KEDA observes workflow/activity backlog and scales matching Deployments from
   zero to one. Native HPA/controller ownership manages idle return to zero.

Kyverno registers resources; it does not create the workload/scaler objects.
Discovery proves registration, not active capacity. Temporal cached poller entries
are not evidence of live Kubernetes Pods.

## Responsibilities

- `internal/plan`: shared parsing, validation, canonical nodes/hash.
- `internal/manifest`: offline graph extraction used by Go tests, not a CLI.
- `internal/registry`: live ConfigMap decoding, synchronization barriers, immutable
  snapshots and shared snapshot validation for GET graph and new-run admission.
- `services/orchestrator/runs`: durable idempotency, Temporal adapter and admission.
- `services/orchestrator/workflows`: deterministic dependency-aware parent.
- `services/worker`: child workflow and activity implementation.
- `internal/process`: SDK/HTTP lifecycle, readiness, fatal notifications and shutdown.
- `scripts/local.sh`: owned lifecycle/tunnels. `k8s-up.sh`: internal bootstrap.
- `scripts/expected-workers.jq`: readiness projection of rendered resources, saved
  in local lifecycle state; never a static registry or execution fallback.
- `scripts/wait-graph.sh`: live graph polling with a 120-second elapsed budget.

## Validation boundaries

Policy admission checks local Service annotation syntax. Go graph building checks
the complete discovered graph. Offline chart/manifest Go tests validate the
intended deployment; Kubernetes server dry-run validates resource schemas before
apply. No `cmd/validate-graph` command or alternate registry mode remains.

A run's immutable plan survives later registration changes, but that does not keep
worker Deployments alive. Do not remove or incompatibly change workers during
active execution. Native retries remain important for worker interruption.

## Persistence and lifecycle

Temporal server and visibility databases use PostgreSQL backed by a local PVC.
Bootstrap Jobs initialize compatible schemas and the `default` namespace before
app polling. A retained PVC must remain paired with its immutable credential
Secret. Local storage is not an external backup.

`down` stops the verified kind node and managed localhost tunnels while retaining
cluster/storage. It interrupts work rather than pausing workflow deadlines.
`up` resumes installed workloads; it does not rebuild or migrate them.
`destroy` deletes only the verified owned demo and local lifecycle records.

Identity checks, per-checkout locking and exact tunnel PID/start identity prevent
accidental mutation of unrelated resources. An existing unowned cluster is never
silently adopted. See [README](../README.md) for upgrade/reset limitations and
[testing](testing.md) for the retained correctness checks.
