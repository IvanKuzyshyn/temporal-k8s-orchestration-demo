# Temporal Kubernetes orchestration demo — design

Status: Approved
Date: 2026-10-17
Repository: `IvanKuzyshyn/temporal-k8s-orchestration-demo`

## 1. Purpose

Build a small, reproducible local experiment demonstrating how Temporal coordinates independently deployed workers, KEDA activates workers from zero replicas, and Kyverno discovers worker services through generated Kubernetes ConfigMaps.

The main learning goal is the interaction among these systems, not production infrastructure or a general-purpose workflow platform. A user should be able to create a kind cluster, deploy the stack, trigger an HTTP endpoint, observe dependency-aware execution, and see workers return to zero replicas.

## 2. Agreed requirements

- Run the complete integration locally on Kubernetes using kind.
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

- One application namespace and one Temporal namespace for the initial demo; dependency names are Kubernetes Service names within the application namespace.
- One always-running orchestrator replica containing both the HTTP API and the parent-workflow worker.
- One reusable worker binary and image, deployed as several logical workers.
- One shared child workflow type and activity contract across all example workers.
- A single Temporal development-server instance for the first iteration. Persistence is optional and its behavior must be documented; this is not a production Temporal deployment.
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

The worker identity is the Service name. For the initial schema, the task queue is derived as `<service-name>.v1.temporal`, and the child workflow type is fixed as `ExampleChildWorkflow`. These are application contracts, not features inferred from Temporal.

Dependencies are parsed by splitting on commas and trimming whitespace. Missing or wholly empty values mean no dependencies. Duplicate names are deduplicated; empty entries in a nonempty list are rejected to catch malformed declarations. References to unknown workers, self-dependencies, and cycles invalidate the graph.

Kubernetes allows at most 256 KiB for the aggregate size of annotation keys and values on an object. The small dependency lists in this project are well below that limit.

### Layered dependency validation

Validation has three complementary layers:

1. **Kyverno admission:** enforce annotation syntax and reject self-dependencies on source Service CREATE and UPDATE requests, before the resource is persisted. Split and trim dependency entries before comparing them with `metadata.name`. Apply enforcement to registered worker Services in the application namespace and return actionable rejection messages. Permit absent/empty dependency annotations for roots and permit disabling/removing registration so cleanup can proceed.
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
  sourceServiceUID: "<source Kubernetes Service UID>"
```

The ConfigMap is derived state, not intended for manual editing. Kyverno must have the narrowly scoped permissions required to generate and synchronize it. The orchestrator only needs read access to registration ConfigMaps in the application namespace; granting access to Services for stale-source validation is a fallback, not a default.

### Lifecycle requirements

- Service creation eventually creates a registration.
- Annotation updates eventually update its registration.
- Deleting a Service or removing/disabling its registration annotation eventually removes its registration.
- Existing annotated Services are reconciled when the policy is installed.
- Ordinary Deployments and Pods never generate registrations.

Verify these behaviors against the pinned Kyverno release. Synchronization, source deletion, and a source no longer matching policy conditions are distinct cases and must not be assumed equivalent. If native generation cannot satisfy the lifecycle contract, explicitly choose a cleanup policy or source-validation mechanism before implementing discovery; do not silently retain stale entries.

### Orchestrator registry

Use a Kubernetes list/watch mechanism, including initial synchronization, reconnect/relist handling, and deletion events. Publish immutable, validated registry snapshots to request handlers. Sort worker identities and dependency lists so the plan is reproducible.

Each snapshot includes a content hash. Invalid registry state rejects new runs rather than silently using a partial graph. Existing runs retain their original plan. Expose registry contents, hash, and validation errors through `GET /workers` for debugging. An empty registry is not a runnable plan.

Discovery is eventually consistent. Deployment and smoke-test automation waits until the expected valid registry is visible before starting a run.

## 6. Orchestrator and execution semantics

### HTTP API

- `POST /runs`: start one execution of the entire current valid graph; return HTTP 202 with workflow ID, run ID, and plan hash. Accept a small example payload and an optional request/idempotency key.
- `GET /runs/{workflowID}`: return Temporal execution status and the associated run ID; include a result or failure summary when available. IDs identify one logical run; workflow ID reuse is disabled for this demo.
- `GET /workers`: expose the current registry and validation status.
- `GET /healthz` and `GET /readyz`: process health and readiness.

Return a clear client error for malformed input and a service-unavailable response when no valid synchronized registry or Temporal connection is available. The API must not return a successful start if Temporal rejected it. Repeated requests with the same idempotency key map to the same workflow identity; conflicting reuse with different input must be rejected or explicitly documented, not silently start additional work.

### Plan snapshot and determinism

Validate and snapshot the graph before starting the parent workflow. Pass the immutable execution plan, its hash, and example payload as workflow input. Do not read Kubernetes, perform HTTP calls, access wall-clock time, or consult mutable process state directly in workflow code.

Use Temporal workflow APIs for time, concurrency, and waiting. Avoid unsorted Go map iteration in workflow decisions. Configuration changes affect only newly started runs.

### Scheduling

Run every root child concurrently. Start another child only after all of its declared dependencies have completed successfully. Schedule a newly ready child immediately rather than imposing global execution barriers between graph levels.

Use stable child workflow IDs derived from the parent workflow identity and worker identity. Each child runs on its worker's stable task queue and executes the example activity on that same queue. Register both workflow and activity handlers in the worker process.

Proposed failure behavior: a terminal child failure fails the parent, starts no further children, and requests cancellation of already-running siblings. Configure parent-close behavior explicitly to request cancellation of unfinished children. Cancellation is cooperative and cannot undo completed side effects. Demonstration activities should heartbeat when long-running and respond to cancellation.

Use bounded activity retries and explicit execution timeouts. Allow enough time for KEDA activation and pod startup. Do not enable whole-parent retries initially; activity retry behavior is sufficient to demonstrate recovery without repeating the whole graph.

### Worker behavior

The shared worker binary takes worker identity, Temporal endpoint/namespace, task queue, and simulated activity duration from configuration. A chi server exposes health/readiness. Example activity output includes worker identity and input; logs include workflow, run, activity, and queue identifiers.

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
  kind/                  Cluster configuration
  infrastructure/        Temporal, KEDA, Kyverno configuration
  policies/              Registration generation and validation
  apps/                  Orchestrator and worker resources
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

Use mise tasks to create/delete the cluster, install infrastructure, build/load images, deploy applications, port-forward, run tests, and run the smoke scenario. Installation must wait for CRDs, controllers, admission webhooks, and Temporal namespace readiness before applying dependent resources.

Document both a quick-start path and teardown. Clarify which teardown operations delete workflow history. Keep secrets and local state out of Git. Restrict local endpoints to localhost; production authentication is not part of this demo.

## 9. Demonstration and acceptance criteria

The main graph is a diamond: A has no dependencies; B and C depend on A; D depends on both B and C. This proves roots, parallel execution, and multi-parent joins with four deployments of the same worker binary.

A successful demonstration proves:

1. The cluster and infrastructure can be created using documented commands.
2. Four annotated Services produce four valid registration ConfigMaps.
3. All four worker Deployments can start at zero replicas while the orchestrator remains available.
4. An API request returns workflow/run IDs and a plan hash.
5. A activates and finishes; B and C then become eligible concurrently; D starts only after both succeed.
6. The parent completes and its result is retrievable.
7. Workers eventually return to zero after completion and configured cooldown.
8. Dependency updates change subsequent runs, not an already-started run.
9. Invalid observed graphs reject new runs with actionable diagnostics before any Temporal workflow starts.
10. Registration removal and Service deletion leave no usable stale registration after reconciliation.
11. Kyverno rejects malformed dependency lists and self-references on both Service creation and update, including whitespace-padded self-references, while allowing valid roots and multi-dependency lists.
12. CI/pre-deployment validation rejects unknown dependencies and cycles of arbitrary length in the intended graph; orchestrator validation also catches such invalid graphs introduced outside that path.

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

Keep lightweight checks on every pull request. The heavier kind test can begin as a manually triggered CI workflow, then become required once stable. Clearly separate expected recovery through retries from a claim that scale-down never interrupts work.

## 11. Risks and mitigations

- **Premature scale-down:** short idempotent activities, conservative cooldown, graceful shutdown, retries, and optional running-workflow signal. Residual risk remains; measure it.
- **Stale discovery:** synchronized generation, tested deletion semantics, list/watch recovery, and observable registry state.
- **Admission blind spots and concurrent changes:** enforce local syntax/self-reference rules with Kyverno, validate the full intended graph in CI, and validate observed snapshots before workflow starts. Do not assume cross-resource lookups are transactional or admission checks repair existing resources.
- **Nondeterministic orchestration:** immutable workflow input and Temporal-safe scheduling; do not perform discovery inside workflow code.
- **Unavailable worker:** validate deployment/queue naming, inspect scaler errors, and bound execution timeouts. Discovery proves registration, not executable capacity.
- **Removal during execution:** prohibit it initially; future work can explore draining and worker deployment versions. A plan snapshot does not keep a Deployment alive.
- **Version or architecture incompatibility:** pin a tested matrix and verify ARM64 images where required.
- **Laptop resource pressure:** use a minimal single-node setup and development Temporal server. Roughly 4–6 available CPU cores and 8–12 GB container-runtime memory is a starting estimate to measure, not a requirement guarantee.
- **Registry abuse:** restrict registration to the application namespace, validate annotations, and use least-privilege service accounts. Ability to create or modify registration sources is a trusted capability.

## 12. Development sequence

1. Implement Temporal parent/child/activity behavior with a static graph in Compose.
2. Add dependency validation, concurrent scheduling, API status, and plan snapshots.
3. Deploy to kind with workers fixed at one replica.
4. Prove KEDA activation and scale-down for a single worker; investigate long-running activity interruption before expanding.
5. Add Kyverno admission validation and generated registrations; test rejection behavior and the entire registration lifecycle. Wire complete-manifest graph validation into CI/pre-deployment tasks.
6. Run the diamond scenario, resilience experiments, CI checks, and document observations.

Each stage should have an independently runnable verification command. Do not debug discovery and scaling for the first time simultaneously.

## 13. Decisions to verify before implementation

No additional product requirements are needed to prepare an implementation plan if the proposed defaults are approved. The implementation plan must settle and record:

- Exact Go, Temporal server/SDK, kind/Kubernetes, KEDA, Kyverno, and chart versions.
- Kyverno support for the specified registration lifecycle and any necessary cleanup mechanism.
- Development-server persistence configuration and reset behavior.
- Initial timeout, retry, shutdown, polling, and cooldown values.
- Precise HTTP request/response schemas and idempotency-conflict handling.

These are implementation choices or compatibility checks, not claims that the stack has already been deployed or tested.

## References

- Temporal Go SDK: https://docs.temporal.io/develop/go
- KEDA Temporal scaler: https://keda.sh/docs/latest/scalers/temporal/
- Kyverno generate rules: https://kyverno.io/docs/policy-types/cluster-policy/generate/
- Kubernetes annotations: https://kubernetes.io/docs/concepts/overview/working-with-objects/annotations/
- kind: https://kind.sigs.k8s.io/
