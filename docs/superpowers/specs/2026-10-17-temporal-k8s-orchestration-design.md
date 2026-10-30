# Temporal Kubernetes orchestration demo — design

Status: Approved
Date: 2026-10-17
Repository: `IvanKuzyshyn/temporal-k8s-orchestration-demo`

## 1. Purpose

Build a small, reproducible local experiment demonstrating how Temporal coordinates independently deployed workers, KEDA activates workers from zero replicas, and Kyverno discovers worker services through generated Kubernetes ConfigMaps.

The main learning goal is the interaction among these systems, not production infrastructure or a general-purpose workflow platform. A user should be able to create a kind cluster, deploy the stack, trigger an HTTP endpoint, observe dependency-aware execution, and see workers return to zero replicas.

## 2. Agreed requirements

- Run the complete integration locally on Kubernetes using kind.
- Place every worker service in its own Kubernetes namespace and the orchestrator in a separate namespace.
- Use PostgreSQL-backed self-hosted Temporal for local development, not the SQLite-based `temporal server start-dev` command.
- Every implementation task must have a definition of done, reproducible local verification, and its own implementation PR. Introduce CI with the first task and extend it incrementally.
- Use Temporal for parent and child workflow orchestration.
- Implement an orchestrator and a reusable worker service in Go, using the Temporal Go SDK and chi.
- Give each logical worker a stable task queue, such as `<service-name>.v1.temporal`. Queues are shared across runs, not created per execution.
- Deploy each logical worker separately and scale its Deployment between zero and one replica using KEDA.
- Discover Kubernetes Services annotated with `temporal-worker: "true"`.
- Express dependencies using `temporal-worker-run-after: "worker-a,worker-b"`.
- Use Kyverno to generate and synchronize one registration ConfigMap per worker Service, rather than aggregating registrations into one shared ConfigMap.
- Use Kyverno admission validation to reject malformed registration annotations and self-dependencies on worker Service creation and update.
- Validate the complete intended dependency graph in CI/pre-deployment checks, including unknown workers and arbitrary cycles.
- Have the orchestrator build and validate the observed dependency graph from those ConfigMaps before accepting a run.
- Provide basic CI, Kubernetes configuration, Compose configuration, documentation, `.gitignore`, `mise.toml`, and a root README.

## 3. Proposed defaults and non-goals

The following are design recommendations, not previously agreed requirements:

- Orchestrator namespace `temporal-demo-orchestrator`; worker namespaces `temporal-demo-worker-a` through `temporal-demo-worker-d`, each holding one logical worker service. Use one Temporal namespace, `default`, independent of Kubernetes namespaces.
- Worker namespaces carry `temporal-demo.io/worker-namespace: "true"`. Discover these namespaces dynamically; dependencies remain globally unique Service names across the demo's worker namespaces. Reject duplicate worker identities rather than resolving them ambiguously.
- One always-running orchestrator replica containing both the HTTP API and the parent-workflow worker.
- One reusable worker binary and image, deployed as several logical workers.
- One shared child workflow type and activity contract across all example workers.
- A minimal single-instance self-hosted Temporal server backed by PostgreSQL for both default and visibility persistence, with pinned schema setup/migration tooling. Run PostgreSQL on a named volume in Compose and a PVC in kind. This is a development setup, not production HA.
- One root Go module, with the service implementations under `services/`.
- No public ingress: access the API and Temporal UI with port-forwarding.
- No worker removal or incompatible worker upgrade while demonstration runs are active.

Out of scope: high availability, production security hardening, arbitrary user-defined workflows, runtime creation of worker Deployments, multi-tenant discovery, and production-grade worker version management. Kyverno discovers already deployed services; it does not provision application code.

## 4. Architecture

```text
User -> Orchestrator HTTP API -> Temporal parent workflow
               |                         |
               | registry snapshot       +-> child A -> queue A -> worker A
               |                         +-> child B -> queue B -> worker B
               v
       Labeled registration ConfigMaps
               ^
               | generated/synchronized by Kyverno
       Annotated worker Services

KEDA -> Temporal queue state -> scales each worker Deployment 0..1
```

Temporal stores workflow state and dispatches tasks. Workers poll Temporal over outbound connections. Kubernetes Services are registration sources and provide health endpoints; Temporal does not route tasks through these Services.

Each worker has a Service, Deployment, and ScaledObject. Their names and queue configuration are produced from one deployment configuration to avoid drift. The parent uses a separate, stable orchestration queue. Temporal task queues are created implicitly when used; no explicit queue-provisioning API is needed.

## 5. Worker registration and discovery

### Service annotations

```yaml
metadata:
  name: worker-c
  annotations:
    temporal-worker: "true"
    temporal-worker-run-after: "worker-a,worker-b"
```

The worker identity is the Service name and must be unique across registered worker namespaces. Namespace separation does not isolate Temporal task queues; reject duplicate identities before starting workflows. For the initial schema, the task queue is derived as `<service-name>.v1.temporal`, and the child workflow type is fixed as `ExampleChildWorkflow`. These are application contracts, not features inferred from Temporal.

Dependencies are parsed by splitting on commas and trimming whitespace. Missing or wholly empty values mean no dependencies. Duplicate names are deduplicated; empty entries in a nonempty list are rejected to catch malformed declarations. References to unknown workers, self-dependencies, and cycles invalidate the graph.

Kubernetes allows at most 256 KiB for the aggregate size of annotation keys and values on an object. The small dependency lists in this project are well below that limit.

### Layered dependency validation

Validation has three complementary layers:

1. **Kyverno admission:** enforce annotation syntax and reject self-dependencies on source Service CREATE and UPDATE requests, before the resource is persisted. Split and trim dependency entries before comparing them with `metadata.name`. Apply enforcement to registered worker Services in labeled worker namespaces and return actionable rejection messages. Permit absent/empty dependency annotations for roots and permit disabling/removing registration so cleanup can proceed.
2. **CI/pre-deployment:** validate the complete intended worker graph from rendered application manifests using the same parsing and graph-validation contract as the orchestrator. Reject unknown dependencies, self-dependencies, and cycles of arbitrary length. Validate a complete deployment set, not an isolated manifest fragment.
3. **Orchestrator:** validate the observed registry after changes and validate the immutable snapshot selected for each request before starting any Temporal workflow. Reject new runs when the graph is invalid; do not defer detection until workflow execution. Keep these checks even when admission and CI validation pass.

Dependency-existence checks through Kubernetes API lookups are optional and disabled initially: enforcing them would make resource application order significant, and a multi-resource manifest or Helm release is not an atomic admission transaction.

General cycle detection is not delegated to Kyverno. Standard declarative rules are not a natural fit for unbounded graph traversal; checking mutual references or a fixed maximum cycle length would leave longer cycles undetected. Cross-resource admission checks also cannot guarantee graph integrity under concurrent updates, since each request may see a state that excludes the other request. CI validates intended state, while the orchestrator protects runs against invalid observed state, including transient partial reconciliation.

Admission enforcement is not retroactive. Use background policy reporting where supported to identify existing violations and remediate them separately; the orchestrator must still reject invalid registrations. A custom graph-validating admission webhook and concurrency coordination are outside this demo's scope.

### Generated ConfigMaps

Kyverno generates one ConfigMap per annotated Service in the same namespace. Proposed name: `temporal-worker-<service-name>`. Each generated registration carries the label `temporal-demo.io/registration: "true"` and an explicit schema version.

Proposed data fields, all stored as strings:

```yaml
data:
  schemaVersion: "1"
  workerName: "worker-c"
  taskQueue: "worker-c.v1.temporal"
  workflowType: "ExampleChildWorkflow"
  runAfter: "worker-a,worker-b"
  sourceServiceName: "worker-c"
  sourceServiceNamespace: "temporal-demo-worker-c"
  sourceServiceUID: "<source Kubernetes Service UID>"
```

The ConfigMap is derived state, not intended for manual editing. Kyverno must have the narrowly scoped permissions required to generate and synchronize it in each worker namespace. The orchestrator service account lives in its own namespace: a read-only ClusterRole permits Namespace get/list/watch, while Roles and RoleBindings in each worker namespace permit ConfigMap get/list/watch for that service account. Do not grant cluster-wide ConfigMap reads or access to Secrets/writes. Namespace labels select discovery scope, not authorization; deployment automation must create the corresponding RoleBinding for every added worker namespace. Granting access to Services for stale-source validation is a fallback, not a default.

### Lifecycle requirements

- Service creation eventually creates a registration.
- Annotation updates eventually update its registration.
- Deleting a Service or removing/disabling its registration annotation eventually removes its registration.
- Existing annotated Services are reconciled when the policy is installed.
- Ordinary Deployments and Pods never generate registrations.

Verify these behaviors against the pinned Kyverno release. Synchronization, source deletion, and a source no longer matching policy conditions are distinct cases and must not be assumed equivalent. If native generation cannot satisfy the lifecycle contract, explicitly choose a cleanup policy or source-validation mechanism before implementing discovery; do not silently retain stale entries.

### Orchestrator registry

Watch labeled Namespaces and maintain a separate registration ConfigMap list/watch per selected namespace, including initial synchronization, reconnect/relist handling, and deletion events. Namespace additions start a watcher; deletion or label removal stops it and removes its registrations. Publish the combined registry as synchronized only after all selected namespace caches synchronize. Permission failures must invalidate synchronization rather than silently omit a worker namespace. Publish immutable, validated registry snapshots to request handlers. Sort worker identities and dependency lists so the plan is reproducible.

Each snapshot includes a content hash. Invalid registry state rejects new runs rather than silently using a partial graph. Existing runs retain their original plan. Expose registry contents, hash, and validation errors through `GET /workers` for debugging. An empty registry is not a runnable plan.

Discovery is eventually consistent. Deployment and smoke-test automation waits until the expected valid registry is visible before starting a run.

## 6. Orchestrator and execution semantics

### HTTP API

- `POST /runs`: start one execution of the entire current valid graph; return HTTP 202 with workflow ID, run ID, and plan hash. Accept no request body (zero bytes) and an optional `Idempotency-Key` header; reject unexpected bytes with a bounded probe and HTTP 400. No JSON decoding or content-type requirement.
- `GET /runs/{workflowID}`: return Temporal execution status and the associated run ID; include a result or failure summary when available. IDs identify one logical run; workflow ID reuse is disabled for this demo.
- `GET /workers`: expose the current registry and validation status.
- `GET /healthz` and `GET /readyz`: process health and readiness.

Return a clear client error for unexpected body bytes or malformed keys. Return service-unavailable for new starts without a valid synchronized registry, or when Temporal is unavailable. The API must not return a successful start if Temporal rejected it. Repeated requests with the same idempotency key return the original durable workflow/run identity, plan hash and available result, including after completion, restart or registry changes. Look up existing keyed runs before validating the current graph; registry changes never conflict with or rebind a key. Store and validate planHash Memo only, with no request hashing or input-conflict handling. Deduplication lasts within Temporal history retention; a new key selects the current validated plan.

### Plan snapshot and determinism

Validate and snapshot the graph before starting the parent workflow. Pass only the immutable execution plan (including its hash) as parent workflow input. Child and activity inputs carry only worker identity; results carry worker identity, never business data. Do not read Kubernetes, perform HTTP calls, access wall-clock time, or consult mutable process state directly in workflow code.

Use Temporal workflow APIs for time, concurrency, and waiting. Avoid unsorted Go map iteration in workflow decisions. Configuration changes affect only newly started runs.

### Scheduling

Run every root child concurrently. Start another child only after all of its declared dependencies have completed successfully. Schedule a newly ready child immediately rather than imposing global execution barriers between graph levels.

Use stable child workflow IDs derived from the parent workflow identity and worker identity. Each child runs on its worker's stable task queue and executes the example activity on that same queue. Register both workflow and activity handlers in the worker process.

Proposed failure behavior: a terminal child failure fails the parent, starts no further children, and requests cancellation of already-running siblings. Configure parent-close behavior explicitly to request cancellation of unfinished children. Cancellation is cooperative and cannot undo completed side effects. Demonstration activities should heartbeat when long-running and respond to cancellation.

Use bounded activity retries and explicit execution timeouts. Allow enough time for KEDA activation and pod startup. Do not enable whole-parent retries initially; activity retry behavior is sufficient to demonstrate recovery without repeating the whole graph.

### Worker behavior

The shared worker binary takes worker identity, Temporal endpoint/namespace, task queue, and simulated activity duration from configuration. A chi server exposes health/readiness. Example activity output includes only worker identity; logs include workflow, run, activity, and queue identifiers.

Activities must be safe to retry. Any later real external side effect must use an idempotency key. Graceful shutdown stops accepting new work and gives in-flight activities a bounded chance to finish before Kubernetes termination.

## 7. KEDA scaling

Use KEDA's native Temporal scaler, available starting with KEDA 2.17. Pin a tested release and check that release's scaler schema and Temporal compatibility rather than relying on `latest` documentation.

Each worker's ScaledObject targets its Deployment with:

- `minReplicaCount: 0` and `maxReplicaCount: 1`.
- Its stable queue and Temporal namespace.
- `activationTargetQueueSize: "0"` and `targetQueueSize: "1"`.
- Workflow and activity queue monitoring, because one worker handles both task types.
- A configurable polling interval, initially around five seconds.
- A conservative cooldown, initially around two minutes for short example activities.

These time values are proposed defaults to validate experimentally, not correctness guarantees. KEDA handles activation from zero and return to zero; the HPA handles ordinary scaling above zero. Do not create another HPA targeting the same Deployment. Deployment automation must not continually reset replicas against KEDA's decisions.

### Safe scale-down limitations

Zero backlog does not imply zero work: an activity may already be executing. Cooldown only reduces the chance of premature termination. Configure a Temporal worker shutdown grace period and a longer Kubernetes termination grace period, and test termination during an activity.

Evaluate `includeRunningWorkflowCount` if supported by the selected KEDA release. Visibility counts are eventually consistent and approximate; they are not a strict guarantee against premature scale-down. More rigorous protection could add a worker-utilization signal, but is outside the initial implementation unless experiments show it is necessary.

A delayed activity retry, timer, or new workflow task can require a previously stopped worker to reactivate. Monitor both task types and include these scenarios in integration tests. Scaling errors must be visible rather than mistaken for an empty queue.

## 8. Local environments and repository layout

Compose supports the fast application-development loop: Temporal, the orchestrator, and example workers with a static registry loaded from a file. Use the same plan schema and validator as the Kubernetes registry adapter. Compose does not emulate Kyverno or KEDA; workers remain running there.

Kind is the authoritative integration environment. Start with one node, preload locally built images, and pin Kubernetes, Helm chart, image, and tool versions. Prefer an ordinary Helm/Kustomize deployment approach rather than introducing a GitOps controller for this demo. Use image tags tied to source revisions to avoid stale local images.

```text
.github/workflows/       Go and manifest/policy validation
k8s/
  infrastructure/        kind input; Temporal, KEDA and Kyverno plain manifests
  policies/              Registration generation and validation
  worker-helm-chart/     Orchestrator and worker Helm templates/values
services/
  orchestrator/          chi API, registry adapters, parent workflow
  worker/                Worker process, child workflow, activity
internal/                Shared contracts and graph validation as needed
deploy/
  compose.yaml           Fast local development environment
  config/                Static example registry and local configuration
docs/
  architecture.md
  discovery.md
  autoscaling.md
  experiments.md
  superpowers/specs/     Design documents
.gitignore
mise.toml
README.md
go.mod
go.sum
```

Use `mise run k8s:up` to create the named cluster if absent, apply per-component plain infrastructure YAML, build/load images, validate the exact application render and apply it. `mise run k8s:down` explicitly deletes the demo cluster and its history. Keep port-forward, validation and integration-test tasks independent; do not add granular provisioning wrappers. This layout/lifecycle supersedes the original fragmented Task 6 provisioning approach (approved PR8 simplification, 2026-10-30). Installation must wait for CRDs, controllers, admission webhooks, PostgreSQL readiness, default/visibility schema setup, and Temporal namespace readiness before applying dependent resources. Schema jobs must be repeatable, version-pinned, and complete before starting Temporal. Keep database credentials in local Secrets/environment files, not committed manifests.

Document both a quick-start path and teardown. PostgreSQL volume/PVC storage preserves history across Temporal and database Pod/container replacement. Compose down preserves named volumes; explicit reset deletes them. Deleting the kind cluster deletes its local PVC data; this is not host-external backup. Verify persistence and document destructive operations. Keep secrets and local state out of Git. Restrict local endpoints to localhost; production authentication is not part of this demo.

## 9. Demonstration and acceptance criteria

The main graph is a diamond: A has no dependencies; B and C depend on A; D depends on both B and C. This proves roots, parallel execution, and multi-parent joins with four deployments of the same worker binary.

A successful demonstration proves:

1. The cluster and infrastructure can be created using documented commands.
2. Four annotated Services produce four valid registration ConfigMaps.
3. All four worker Deployments can start at zero replicas while the orchestrator remains available.
4. A body-free POST returns workflow/run IDs and a plan hash; unexpected body bytes return 400. Same-key retries return the original durable run/plan/result after completion, restart or registry changes.
5. A activates and finishes; B and C then become eligible concurrently; D starts only after both succeed.
6. The parent completes and its sorted worker-identity-only result is retrievable; parent/child/activity commands contain no business data.
7. Workers eventually return to zero after completion and configured cooldown.
8. Dependency updates change subsequent runs, not an already-started run.
9. Invalid observed graphs reject new runs with actionable diagnostics before any Temporal workflow starts.
10. Registration removal and Service deletion leave no usable stale registration after reconciliation.
11. Kyverno rejects malformed dependency lists and self-references on both Service creation and update, including whitespace-padded self-references, while allowing valid roots and multi-dependency lists.
12. CI/pre-deployment validation rejects unknown dependencies and cycles of arbitrary length in the intended graph; orchestrator validation also catches such invalid graphs introduced outside that path.
13. Each worker and the orchestrator run in separate namespaces. Namespace add/remove and RoleBinding changes are reflected in discovery without an orchestrator restart; unauthorized reads/writes are denied.
14. Completed workflow history survives Temporal and PostgreSQL container/Pod replacement; explicit volume/PVC/cluster reset has the documented destructive behavior.

The timing budget for the smoke test must account for controller reconciliation, scaler polling, scheduling, image startup, and cooldown. Assert ordering from workflow events or recorded results, not simply log arrival times.

## 10. Tests and CI

- Go unit tests for parsing, graph validation, deterministic plan construction, and registry hashing.
- Temporal SDK tests for chains, diamonds, child failures, cancellation, and snapshot behavior. These do not prove real scaler integration.
- API tests for valid starts, unavailable/invalid registry state, malformed requests, and idempotency behavior.
- Kyverno admission tests for CREATE and UPDATE: reject malformed lists and self-references (including whitespace-padded names); accept roots, valid multi-dependency lists, and registration disabling/removal. Verify rejection occurs on the source Service, not only on its generated ConfigMap.
- Kyverno policy tests plus real-cluster lifecycle tests for create/update/delete/annotation removal and preexisting resources. Verify existing invalid registrations cannot start workflows even though admission enforcement is not retroactive.
- CI/pre-deployment graph validation against the complete rendered worker manifest set, with fixtures for missing dependencies, two-node cycles, and longer cycles. Reuse the orchestrator's Go graph validator where possible.
- Registry/API integration tests proving partial reconciliation and invalid graphs resulting from concurrent changes block new starts before Temporal is called.
- Manifest validation against pinned CRD schemas; basic formatting, lint, and image-build checks.
- Kind smoke test for zero-to-one activation, dependency order, successful completion, and return to zero.
- Resilience experiments: stop a worker during an activity, exercise delayed retries, restart the orchestrator during a run, and make Temporal temporarily unavailable.

Add CI in the first implementation task: formatting, Go tests/race detection, and vet, using the same commands developers run locally. Extend it in each PR as functionality appears: workflow/API tests, Compose builds/smoke, graph validation, chart/schema validation, kind baseline, scaling, policies, discovery, and full smoke. Each task has its own PR with local verification evidence and explicit completion criteria; previously introduced CI checks remain enabled. Core bounded kind checks run on PRs once introduced; longer resilience experiments can be manual, but remain locally runnable. Clearly separate expected recovery through retries from a claim that scale-down never interrupts work.

## 11. Risks and mitigations

- **Premature scale-down:** short idempotent activities, conservative cooldown, graceful shutdown, retries, and optional running-workflow signal. Residual risk remains; measure it.
- **Stale discovery:** synchronized generation, tested deletion semantics, list/watch recovery, and observable registry state.
- **Admission blind spots and concurrent changes:** enforce local syntax/self-reference rules with Kyverno, validate the full intended graph in CI, and validate observed snapshots before workflow starts. Do not assume cross-resource lookups are transactional or admission checks repair existing resources.
- **Nondeterministic orchestration:** immutable workflow input and Temporal-safe scheduling; do not perform discovery inside workflow code.
- **Unavailable worker:** validate deployment/queue naming, inspect scaler errors, and bound execution timeouts. Discovery proves registration, not executable capacity.
- **Removal during execution:** prohibit it initially; future work can explore draining and worker deployment versions. A plan snapshot does not keep a Deployment alive.
- **Version or architecture incompatibility:** pin a tested matrix and verify ARM64 images where required.
- **Laptop resource pressure:** use a minimal single-node setup and single-instance PostgreSQL-backed Temporal. Roughly 4–6 available CPU cores and 8–12 GB container-runtime memory is a starting estimate to measure, not a requirement guarantee.
- **Registry abuse:** restrict discovery to labeled worker namespaces, validate annotations, and use least-privilege cross-namespace RoleBindings. Restrict who can label Namespaces; the label is a trust declaration, not an RBAC boundary. Ability to create or modify registration sources is a trusted capability.

## 12. Development sequence

1. Establish shared contracts and local test commands with CI in the first PR; implement Temporal parent/child/activity behavior with a static graph in PostgreSQL-backed Compose.
2. Add dependency validation, concurrent scheduling, API status, and plan snapshots.
3. Deploy to kind with workers fixed at one replica.
4. Prove KEDA activation and scale-down for a single worker; investigate long-running activity interruption before expanding.
5. Add Kyverno admission validation and generated registrations; test rejection behavior and the entire registration lifecycle. Wire complete-manifest graph validation into CI/pre-deployment tasks.
6. Run the diamond scenario, resilience experiments, CI checks, and document observations.

Every task must state its definition of done, locally runnable verification commands and expected outcomes, and an implementation PR. Branch each task from updated main after its prerequisite PRs merge; include local results and cumulative CI results before requesting review. Never auto-merge without approval. Each stage should have an independently runnable verification command. Do not debug discovery and scaling for the first time simultaneously.

## 13. Decisions to verify before implementation

No additional product requirements are needed to prepare an implementation plan if the proposed defaults are approved. The implementation plan must settle and record:

- Exact Go, Temporal server/SDK/schema tooling, PostgreSQL, kind/Kubernetes, KEDA, Kyverno, and chart versions.
- Kyverno support for the specified registration lifecycle and any necessary cleanup mechanism.
- PostgreSQL/Temporal persistence compatibility, schema setup/migration versions, PVC configuration, and reset behavior.
- Initial timeout, retry, shutdown, polling, and cooldown values.
- Precise body-free HTTP request/response schemas and durable key-only idempotency handling.

These are implementation choices or compatibility checks, not claims that the stack has already been deployed or tested.

## References

- Temporal Go SDK: https://docs.temporal.io/develop/go
- KEDA Temporal scaler: https://keda.sh/docs/latest/scalers/temporal/
- Kyverno generate rules: https://kyverno.io/docs/policy-types/cluster-policy/generate/
- Kubernetes annotations: https://kubernetes.io/docs/concepts/overview/working-with-objects/annotations/
- kind: https://kind.sigs.k8s.io/
