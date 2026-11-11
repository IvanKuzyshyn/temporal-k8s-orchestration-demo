# Architecture and operational boundaries

## Request to execution

The API and parent poller share one always-on orchestrator process. Workers are
four independent deployments of the same Go binary, each in a separate namespace.
Their stable queues are `worker-a.v1.temporal` through `worker-d.v1.temporal`;
the parent queue is `orchestrator.v1.temporal`. Queues are shared across runs and
created implicitly by Temporal. Kubernetes Services expose health and serve as
registration sources; Temporal tasks travel through outbound worker polling,
**not through Service HTTP routing**.

A zero-byte `POST /runs` optionally supplies an opaque Idempotency-Key. The API
looks for an existing key-derived workflow ID **before** consulting the current
registry. A new run copies and revalidates the selected immutable plan, including
canonical hash, then starts the parent. Memo stores only planHash; no request hash
or business payload exists. Describe/status and starts have two-second operation
budgets. A 503 or transport loss is ambiguous: Temporal may already have accepted
the start. Same-key retry resolves durable identity; a fresh key may duplicate work.
History retention/reset bounds deduplication. See [API](api.md).

The parent receives only a canonical execution plan. It starts roots concurrently,
schedules newly ready children as soon as all their own dependencies succeed,
and sorts decisions rather than iterating Go maps nondeterministically. In the
diamond B/C are both eligible after A; D waits for both. Each child uses a stable
parent/worker-derived ID, its worker queue, and the fixed ExampleChildWorkflow
contract. Child/activity input and output carry only workerName. The parent
returns sorted worker identities. Workflow code uses Temporal time/concurrency
APIs, never Kubernetes, HTTP, wall-clock time or mutable registry state.

Parent execution timeout is 30 minutes; child timeout 15 minutes; neither has
whole-workflow retries. Activities have start-to-close 60 seconds,
schedule-to-close 10 minutes, heartbeat 15 seconds, and up to three attempts
(initial retry 2 seconds, exponential factor 2, maximum interval 10 seconds).
A terminal child failure fails the parent, stops new scheduling and requests
sibling cancellation; parent-close policy explicitly requests cancellation.
Cancellation is cooperative and cannot reverse completed effects. Future external
side effects must be idempotent. Demo duration defaults to 5 seconds and must be
positive and below 60 seconds. Worker Stop grants 30 seconds; Kubernetes grants
45 seconds (Compose 40 seconds). Recovery is not a no-interruption guarantee.

## Registration, consistency and trust

Static Compose/baseline kind use the same plan builder as live discovery.
Kyverno admission validates selected Service annotation syntax and self-reference
on CREATE/UPDATE. It deliberately does not prove dependency existence or arbitrary
cycles across concurrent resources. Complete-render predeployment validation
checks intended graph integrity; observed-registry validation independently blocks
invalid/empty new starts. None is a substitute for the other two.

Kyverno generates one synchronized same-namespace registration ConfigMap per
Service, including schema version and source identity. Create/update/delete,
disable/removal and preexisting-source reconciliation are distinct tested paths.
The orchestrator watches labeled Namespaces and per-namespace labeled ConfigMaps,
including list/watch synchronization, removal, reconnect/relist and permission
changes. It publishes immutable copies. Existing workflow input never changes
when registry contents change. Plan hashing excludes diagnostic source UID.

There is no atomic multi-resource rollout. A partial graph can be valid: operators
must await the expected complete names **and hash** before POST. Readiness requires
synchronization, not graph validity, so invalid observed graphs remain diagnosable
via `/workers` while new starts fail closed. Current keyed history can still replay.

The orchestrator ClusterRole reads only Namespaces; each worker namespace grants
ConfigMap get/list/watch through a RoleBinding to its separate service account.
No cluster-wide ConfigMap reads, Service/Secret reads or writes are granted to it.
A namespace label selects scope, **not authorization**; a missing/revoked binding
invalidates synchronization rather than silently dropping workers. Watch recovery
has bounded grace, not an age limit on idle healthy data.

Namespace labelers and Service/registration writers are trusted principals.
Source UID fields are diagnostic, not cryptographic proof that a ConfigMap came
from a real Service. The pinned Kyverno chart's background controller has broader
ConfigMap write defaults than the orchestrator; [discovery](discovery.md) describes
this hardening limit. Do not treat this local demo as hostile multi-tenant discovery.

## Infrastructure and ownership

PostgreSQL stores both default and visibility histories in separate databases.
Matching schema tooling completes before Temporal; default namespace bootstrap
completes before pollers. Compose uses project-scoped named storage; kind uses a
StatefulSet PVC paired with an immutable Secret. Replacement preserves storage;
volume/PVC/cluster deletion destroys it. This is single-instance local persistence,
not HA or external backup. A missing paired Secret is an error, not permission to
regenerate a password. [README](../README.md) gives supported lifecycle commands.

Helm renders checksum-verified pinned KEDA/Kyverno archives and the application
chart; kubectl applies infrastructure in readiness phases. There are no managed
Helm releases. The intended graph is checked before app Namespace bootstrap;
then strict live API validation and apply consume the same application bytes.
Offline Helm/graph validation cannot validate unknown/unavailable custom APIs.

KEDA's native Temporal scaler monitors workflow **and activity** backlog. Workers
bootstrap at zero once; steady-state Deployment renders omit replicas, and
last-applied ownership is handed off without resetting native decisions. KEDA
owns 0↔1 activation and creates the HPA; do not add a competing HPA or continuously
apply fixed replicas. Polling 5 seconds and cooldown 120 seconds are configuration,
not startup latency guarantees. Backlog zero does not mean no activity is running.
Running-workflow count is opt-in, approximate and visibility-delayed; it cannot
provide strict in-flight protection. Timers/retries may reactivate stopped workers.
Scaler errors must be diagnosed separately from healthy inactivity.

## Verification boundaries

Unit/SDK tests prove parsing, deterministic scheduling and HTTP contracts without
live infrastructure. Compose proves real server/persistence semantics, not KEDA
or Kyverno. Isolated kind suites establish scaling and discovery independently;
combined smoke temporarily combines them, asserts histories/order/payloads and
returns to the fixed/static baseline. Resilience proves bounded recovery under
specific faults, not arbitrary outages or lossless side effects. Its relay fault
isolates orchestrator connectivity, not all Temporal clients.

Experiments register exact start intent before POST and pin resolved run IDs,
including ambiguous failures, before scoped cleanup/history inspection. They must
not broadly terminate workflows. Restoration failure is a failing experiment,
not a warning. [Experiments](experiments.md) maps all fourteen criteria to named
tests and explicitly separates historical Linux CI from current local evidence.
