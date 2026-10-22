# Temporal Kubernetes Orchestration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver a reproducible local diamond-workflow demo with Temporal, independently scaled Go workers, KEDA, and Kyverno-driven registration.

**Architecture:** A chi API snapshots a validated worker registry and starts a Temporal parent workflow. The parent schedules dependency-ready child workflows on stable queues; KEDA activates individual worker Deployments and Kyverno generates a ConfigMap for each registered Service. Compose uses a static registry adapter; kind exercises actual discovery and scaling.

**Tech Stack:** Go, Temporal Go SDK and PostgreSQL-backed self-hosted Temporal server, chi, client-go, Docker Compose, kind, Helm, KEDA, Kyverno, mise, GitHub Actions.

**Spec:** [Approved design](../specs/2026-10-17-temporal-k8s-orchestration-design.md). Read both documents before implementation.

## Global Constraints

- Stable queue convention: `<service-name>.v1.temporal`; fixed child workflow type: `ExampleChildWorkflow`.
- Registration annotations: `temporal-worker: "true"` and `temporal-worker-run-after: "worker-a,worker-b"`.
- ConfigMap naming: `temporal-worker-<service-name>`; label: `temporal-demo.io/registration: "true"`; schema version: `"1"`.
- Split comma-separated dependencies, trim whitespace, deduplicate names, allow absent/wholly empty values, and reject empty entries in nonempty lists.
- Reject unknown workers, self-dependencies, duplicate identities, and arbitrary cycles before starting a workflow.
- Kyverno enforces local syntax/self-reference rules, not arbitrary cycles or dependency existence.
- KEDA Temporal scaler version floor: 2.17; `minReplicaCount: 0`, `maxReplicaCount: 1`, `activationTargetQueueSize: "0"`, `targetQueueSize: "1"`.
- One root Go module; one reusable worker image; one always-running orchestrator replica.
- No discovery I/O in workflow code; run inputs are immutable plans. Compose does not emulate KEDA or Kyverno.
- No production HA, ingress, arbitrary workflow plugins, or worker deletion/incompatible upgrades during active runs.
- Every worker service has its own Kubernetes namespace; the orchestrator has a separate namespace.
- Every implementation task adds tests before behavior, extends cumulative CI as needed, is verifiable locally, and opens its own implementation PR. This document alone does not authorize execution.

## Per-task delivery contract

Start each task on a new branch from updated `main` after prerequisite PRs merge. Each numbered task below is one independently reviewable implementation PR, not merely one commit. Open it with `gh pr create --base main`, including scope, definition-of-done checklist, exact local verification commands/results, and any limitations. Request review only after its local checks and all cumulative CI checks pass. Do not auto-merge or treat an open PR as merged.

Task 1 establishes `.github/workflows/validate.yaml` and `mise run validate`; later tasks extend these without disabling earlier coverage. Every check has a local mise entry point: GitHub-specific credentials, hosted services, and CI-only fixtures must not be required. Commands listed below must be implemented in the owning task. Integration commands provision or document local prerequisites, use bounded waits, and collect diagnostics on failure. A skipped required local test means the task is not done.

Task 4 adds Compose CI, Task 6 adds PR-triggered kind baseline CI, and Tasks 7–10 extend that kind suite. Longer destructive/resilience experiments may use manual CI dispatch, but core regression checks remain automatic on every PR. Pin actions to reviewed commit SHAs, use read-only permissions by default, and never expose deployment credentials to untrusted PRs.

## Review Focus

- Whitespace, duplicate dependencies, and trailing commas must mean the same thing in Go and Kyverno; tested in Tasks 1 and 8.
- Concurrent duplicate HTTP requests and retries after completion must not create extra runs; conflicting payloads must not silently succeed; tested in Task 3.
- Watch reconnects, ConfigMap deletion, invalid partial graphs, and mutation of returned slices must not expose a stale-valid or mutable snapshot; tested in Task 9.
- A fast branch must not wait for unrelated slow branches; failure must not launch downstream work, and cancellation must reach siblings; tested in Task 2.
- Zero backlog during an activity, delayed retry, and a worker killed mid-task must not be misreported as successful uninterrupted execution; tested in Tasks 7 and 10.

---

## Decisions and compatibility gate

Concrete implementation choices:

- Orchestrator namespace `temporal-demo-orchestrator`; one worker namespace per service, `temporal-demo-worker-a` through `temporal-demo-worker-d`. Temporal namespace `default`; parent queue `orchestrator.v1.temporal`; kind cluster name `temporal-demo`.
- Worker namespaces have label `temporal-demo.io/worker-namespace: "true"`. Watch labeled Namespaces dynamically and registrations in each namespace; adding a worker requires its Namespace, Service/Deployment/ScaledObject, and a read-only RoleBinding to the orchestrator service account. Namespaces are discovery scope, not separate Temporal namespaces.
- Keep simple comma-separated dependency names globally unique across the demo. Reject duplicate Service identities across namespaces; do not guess which worker was intended. Generated ConfigMaps stay in their source worker namespace and include `sourceServiceNamespace`.
- Infrastructure namespaces `temporal`, `keda`, and `kyverno`.
- API port 8080; worker health port 8081; host API forwarding bound to `127.0.0.1`.
- Example payload: `{"message":"hello"}`. All four workers return their identity and the unchanged message. A parent result is sorted by worker identity, independent of completion order.
- Graph: A → B/C → D, using Service names `worker-a` through `worker-d`.
- Default activity duration 5 seconds; heartbeat interval 2 seconds; heartbeat timeout 15 seconds; activity start-to-close timeout 60 seconds; schedule-to-close timeout 10 minutes; maximum attempts 3, initial retry interval 2 seconds, maximum interval 10 seconds, coefficient 2.
- Parent execution timeout 30 minutes, child execution timeout 15 minutes, no parent/child workflow retry policy. These leave room for cold starts; configure shorter test-only values.
- Worker stop timeout 30 seconds; Kubernetes termination grace 45 seconds; KEDA polling 5 seconds and cooldown 120 seconds.
- Use a minimal self-hosted Temporal server, not `temporal server start-dev` (which uses SQLite). PostgreSQL backs both default and visibility stores with separate databases, `temporal` and `temporal_visibility`. Pin the compatible PostgreSQL plugin, server, and schema tool versions. Run schema setup/migrations as a one-shot Compose service and Kubernetes Job before server readiness; bootstrap the Temporal namespace afterward.
- PostgreSQL storage uses a named Compose volume and a kind PVC. Container/Pod replacement preserves history. Compose down preserves volumes; explicit reset destroys them. Kind cluster deletion destroys local PVC data; ordinary Temporal/PostgreSQL restarts must not. No external backup or production HA is promised.
- Application resources use one small Helm chart with explicit per-resource namespaces. Its worker values generate Namespace labels, Service annotations, queue environment variables, per-namespace RBAC, and ScaledObjects from the same identity. Each ScaledObject shares its target Deployment's namespace. The Helm release lives in the orchestrator namespace; no separate hand-maintained queue names.
- Image tags use the full Git SHA for clean builds, and an explicit unique local tag for dirty development builds; never reuse `latest`.

**Blocking compatibility gate:** exact tool/chart/image/SDK versions, PostgreSQL persistence/schema compatibility, and Kyverno cleanup behavior have not been validated in this planning session. Do not invent a tested matrix. Before Task 1's dependency installation, select exact released versions from upstream compatibility documentation, record them in `docs/versions.md`, pin tools in `mise.toml`, and pin dependencies as each consuming task is implemented. Verify the matrix on the host architecture and Linux CI. Task 8 must prove cleanup behavior before Kubernetes discovery is enabled. If compatibility changes a design contract, stop and update the design rather than silently weakening it.

For KEDA, the baseline requires `queueTypes: "workflow,activity"` and reliable backlog access with no active pollers. Treat `includeRunningWorkflowCount` as optional: only enable it after verifying support in the pinned release and documenting visibility limitations. Pin chart versions separately from application versions.

## File and interface map

- `internal/plan/{types,parse,validate,hash}.go`: shared registration, canonical graph, and validation.
- `internal/registry/{registry,static,kubernetes}.go`: immutable snapshot providers; Kubernetes decoding/watch logic stays outside workflows.
- `internal/contracts/workflow.go`: serialized parent/child inputs and results, workflow names.
- `services/orchestrator/workflows/parent.go`: deterministic dependency scheduling.
- `services/orchestrator/{api,runs}/`: chi handlers and Temporal-backed start/status behavior.
- `services/orchestrator/cmd/orchestrator/main.go`: wiring and shutdown only.
- `services/worker/{workflows,activities}/`: child wrapper and cancellable example activity.
- `services/worker/cmd/worker/main.go`: polling, health, configuration, shutdown.
- `cmd/validate-graph/main.go`: complete-manifest graph validation using `internal/plan`.
- `tests/integration/`: bounded Go integration tests enabled with build tag `integration`; tests require explicit environment configuration.
- `deploy/`: Dockerfiles, Compose, and static registry fixture.
- `k8s/apps/`: Helm chart for application resources; `k8s/infrastructure/`: pinned controller/server configuration; `k8s/policies/`: policy manifests and fixtures.
- `scripts/`: small shell wrappers for infrastructure/test setup, with `set -euo pipefail` and explicit context checks.
- `mise.toml`: documented local task entry points; `.github/workflows/validate.yaml`: cumulative PR checks from Task 1; `compose.yaml`: added in Task 4; `kind-integration.yaml`: automatic bounded PR integration from Task 6, plus manually dispatched longer experiments.

Shared contracts to define in Task 1:

```go
// package plan
 type Registration struct {
     SchemaVersion string
     WorkerName string
     TaskQueue string
     WorkflowType string
     RunAfter string
     SourceServiceName string
     SourceServiceNamespace string
     SourceServiceUID string
 }
 type Node struct { Name, TaskQueue, WorkflowType string; Dependencies []string }
 type Plan struct { SchemaVersion string; Nodes []Node; Hash string }
 func ParseDependencies(raw string) ([]string, error)
 func Build(registrations []Registration) (Plan, error)

// package registry
 type Snapshot struct { Plan plan.Plan; Synced bool; Problems []string }
 type Provider interface { Snapshot() Snapshot }

// package contracts
 type Payload struct { Message string `json:"message"` }
 type ParentInput struct { Plan plan.Plan; Payload Payload; RequestHash string }
 type ChildInput struct { WorkerName string; Payload Payload }
 type ChildResult struct { WorkerName string; Message string }
 type ParentResult struct { Results []ChildResult }
```

Use explicit lower-camel JSON tags on all serialized fields. `Build` validates schema, identities, names, queue convention, and workflow type, then sorts nodes/dependencies and hashes canonical execution-relevant fields using SHA-256. Source namespaces and UIDs aid diagnostics but do not change the execution plan hash. Validate source namespace metadata against the actual ConfigMap namespace in Kubernetes; reject more than one registered worker per namespace and duplicate worker identities across namespaces. All snapshot access returns deep copies. Static registrations use the same schema; source UIDs may be empty outside Kubernetes.

## Task 1: Shared graph contract, local validation, and initial CI

**Files:** Create `go.mod`, `go.sum`, `mise.toml`, `.gitignore`, `docs/versions.md`, `internal/plan/{types,parse,validate,hash}.go`, `internal/plan/plan_test.go`, `internal/contracts/workflow.go`, `internal/registry/registry.go`, `internal/registry/static.go`, `internal/registry/static_test.go`, `deploy/config/workers.json`, `.github/workflows/validate.yaml`.

**Interfaces:** Produces the shared contracts above and `registry.LoadStatic(path string) (Provider, error)`. Root module path is `github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo`.

- [ ] Record and verify the compatibility matrix described above; bootstrap only the module and tools required to run graph tests.
- [ ] Write `TestParseDependencies` cases: `""` and whitespace → no dependencies; `" worker-b,worker-a,worker-b "` → sorted unique A/B; `"worker-a,"`, `",worker-a"`, and `"worker-a,,worker-b"` → error. Write `TestBuild` for diamond success, empty registry, missing worker, self-reference, duplicate identity, two-node and longer cycles, invalid schema/type/queue, and invalid Service names.
- [ ] Write `TestHashCanonical` asserting input/dependency order does not change hash, dependency changes do, and changing source UID does not. Write `TestSnapshotDeepCopy` asserting mutation of returned nodes/dependencies cannot change future snapshots.
- [ ] Run `go test ./internal/plan ./internal/registry`; expect missing implementation failures, not unrelated tool errors.
- [ ] Implement contracts, canonicalization, graph validation, static loading, and immutable snapshots. Populate the diamond fixture. Ignore local state, binaries, rendered files, credentials, and coverage output.
- [ ] Run `go test -race ./internal/plan ./internal/registry` and `go vet ./internal/...`; require success. Add `mise run test` for `go test -race ./...` and `mise run validate` for formatting, full Go tests/race, and vet. Add PR CI invoking that exact local command; pin actions and use minimal permissions. Include duplicate global identity across distinct namespaces in graph tests.
- [ ] Commit: `feat: add canonical worker graph and static registry`.

**Definition of done:** Canonical graph/static registry behavior passes all fixtures, snapshots are immutable, and the first PR has automatic formatting/test/race/vet checks.

**Local verification:** Go/mise only: `mise run validate` must exit 0; intentionally malformed graph fixtures must fail their validation assertions.

**CI increment:** Create the initial automatic PR workflow; tests do not require Docker or a Temporal server.

- [ ] **PR:** Branch `feat/graph-foundation` from updated main after prerequisites merge; open a dedicated implementation PR with the above local evidence and passing cumulative CI. Request review; do not auto-merge.

## Task 2: Temporal child execution and deterministic parent scheduling

**Files:** Create `services/worker/workflows/child.go`, `services/worker/activities/example.go`, their `_test.go` files, `services/orchestrator/workflows/parent.go`, `services/orchestrator/workflows/parent_test.go`.

**Interfaces:** Consumes Task 1 contracts. Produces `workflows.Parent(ctx workflow.Context, input contracts.ParentInput) (contracts.ParentResult, error)`, worker `workflows.Child(ctx workflow.Context, input contracts.ChildInput) (contracts.ChildResult, error)`, and `activities.Example(ctx context.Context, input contracts.ChildInput) (contracts.ChildResult, error)`. Configure activity duration through an activity receiver/constructor, never mutable workflow globals. Register names `ParentWorkflow`, `ExampleChildWorkflow`, and `ExampleActivity` explicitly.

- [ ] Write Temporal SDK tests for roots in parallel, chain, diamond join, and fast-branch progress while an unrelated root remains unfinished. Assert each child uses its own queue and identity-derived child ID.
- [ ] Write failure/cancellation tests: failed A prevents B/C/D, failed sibling cancels running siblings, parent cancellation propagates, and no whole-parent retry occurs. Test output sorting and activity retry/cancellation/heartbeat behavior.
- [ ] Run `go test ./services/orchestrator/workflows ./services/worker/...`; confirm tests fail before implementation.
- [ ] Implement a sorted-node scheduler with Temporal futures/selectors: launch all ready nodes, wait for a completion, record successes, and launch newly eligible nodes. Never use native goroutines or unordered map traversal to make workflow decisions. Use child cancellation contexts and explicit request-cancel parent-close policy. Bound retries/timeouts with the values above.
- [ ] Implement the child wrapper and cancellable, heartbeating example activity. Validate worker identity against local configuration in activity execution to expose misrouted deployment configuration.
- [ ] Run the package tests with `-race`; require success and verify the fast-branch test would fail under a level-barrier scheduler.
- [ ] Commit: `feat: execute dependency-aware Temporal workflows`.

**Definition of done:** Temporal SDK tests prove parallel roots, immediate dependency scheduling, join ordering, cancellation, and bounded retry behavior without a live server.

**Local verification:** `mise run validate` and `go test -race ./services/orchestrator/workflows ./services/worker/...` must pass locally.

**CI increment:** The Task 1 full-package test command automatically includes new workflow/activity tests; confirm they appear in PR test output.

- [ ] **PR:** Branch `feat/temporal-workflows` from updated main after prerequisites merge; open a dedicated implementation PR with the above local evidence and passing cumulative CI. Request review; do not auto-merge.

## Task 3: HTTP API with durable start deduplication

**Files:** Create `services/orchestrator/runs/{service,temporal}.go`, `services/orchestrator/api/{router,handlers}.go`, corresponding `_test.go` files, and `docs/api.md`.

**Interfaces:** Produces `runs.Service` with `Start(ctx context.Context, key string, payload contracts.Payload) (Run, error)` and `Get(ctx context.Context, workflowID string) (Run, error)`; `Run` contains workflow ID, run ID, plan hash, status, optional `ParentResult`, and failure summary. Constructor consumes `registry.Provider` and a narrow mockable Temporal gateway. `api.New(provider registry.Provider, service *runs.Service, ready func(context.Context) error) http.Handler` returns chi routing.

HTTP contract:
- `POST /runs` accepts exactly `{"message":"..."}`, optional `Idempotency-Key` header, and at most 16 KiB; reject unknown fields, trailing JSON, oversized bodies, and malformed/overlong keys. Keys are 1–128 printable ASCII characters.
- Without a key, generate a unique workflow ID. With a key, ID is `run-` plus SHA-256 of the key. Never include raw key material in logs.
- Hash canonical payload independently of registry version. Store request hash and plan hash in Temporal Memo at workflow creation. Use reject-duplicate workflow ID reuse and conflict-fail semantics supported by the pinned SDK.
- On already-exists, describe the original run, compare request hashes, and return its original identifiers/hash for equal payload; return 409 for conflicting payload. Do not rebind a retry to a changed registry plan. This works after completion and orchestrator restarts, within Temporal history retention. Document that deduplication expires with history; no separate application idempotency database is introduced beyond Temporal's PostgreSQL persistence.
- 202 accepted/repeated same request; 400 malformed request; 413 oversized body; 409 conflicting key; 503 invalid/unsynchronized registry or unavailable Temporal; GET unknown ID → 404; unexpected internal errors → 500 without sensitive details.
- `GET /workers` returns synchronization state, canonical plan/hash, and diagnostics even when invalid. `GET /healthz` is process liveness; `/readyz` checks initialized registry and Temporal connectivity with a bounded timeout, but not graph validity, so diagnostics remain reachable when a graph is invalid.

- [ ] Write handler/gateway tests for these statuses, malformed input, concurrent duplicate submissions, duplicate after completion, payload conflict, registry change on retry, Temporal timeout after an ambiguous start, invalid snapshot, and no Temporal start call on graph rejection.
- [ ] Run `go test ./services/orchestrator/api ./services/orchestrator/runs`; expect missing implementations.
- [ ] Implement API and Temporal gateway; keep describe/result retrieval nonblocking for unfinished runs. Revalidate the copied plan before the first start and attach its immutable input. Resolve duplicate keys from Temporal rather than an in-memory map; if existing-run lookup is unavailable, return 503 rather than inventing success.
- [ ] Run `go test -race ./services/orchestrator/...`; require success. Document retention-bound deduplication and the exact example requests/responses in `docs/api.md`.
- [ ] Commit: `feat: expose workflow run API and idempotent starts`.

**Definition of done:** All HTTP contracts and duplicate-request race cases pass; an invalid graph never invokes Temporal start.

**Local verification:** `mise run validate` and `go test -race ./services/orchestrator/api ./services/orchestrator/runs` pass with fake registry/Temporal gateways; no cluster or hosted Temporal required.

**CI increment:** Keep the existing checks and include the new API/gateway packages in the automatic Go suite.

- [ ] **PR:** Branch `feat/orchestrator-api` from updated main after prerequisites merge; open a dedicated implementation PR with the above local evidence and passing cumulative CI. Request review; do not auto-merge.

## Task 4: Runnable Go processes and Compose vertical slice

**Files:** Create service `cmd/*/main.go` files listed above, `internal/runtimeconfig/config.go` and tests, `deploy/{orchestrator,worker}.Dockerfile`, `deploy/compose.yaml`, `deploy/temporal/config.yaml`, `scripts/{compose-smoke,compose-persistence}.sh`, `.github/workflows/compose.yaml`, `README.md`; modify `mise.toml` and cumulative validation.

**Interfaces:** Environment keys: `TEMPORAL_ADDRESS`, `TEMPORAL_NAMESPACE`, `REGISTRY_MODE=static|kubernetes`, `REGISTRY_FILE`, `WORKER_NAMESPACE_SELECTOR`, `WORKER_NAME`, `TASK_QUEUE`, `ACTIVITY_DURATION`, and `HTTP_ADDR`. Produces `mise run compose:up`, `compose:smoke`, `compose:down`, and explicit destructive `compose:reset`.

- [ ] Write configuration tests rejecting missing identity, mismatched queue, unknown registry mode, and malformed durations. Write a Compose smoke script that waits for readiness, submits a run, polls completion, and asserts four named results. Run tests/script before process wiring; expect failure.
- [ ] Wire Temporal registration, static registry, chi servers, structured logs, startup retries, and SIGTERM shutdown. Mark workers ready only after polling starts; separate liveness from Temporal availability. Log run/workflow/activity/queue identifiers through SDK-aware logging.
- [ ] Build non-root application images and Compose services for PostgreSQL, schema setup, self-hosted Temporal, Temporal UI, orchestrator, and four workers. Bind host ports to localhost. Use pinned server/admin-tools images with a compatible PostgreSQL SQL plugin; initialize both persistence databases with matching schemas. Wait for PostgreSQL health and schema completion before starting Temporal, then create/check Temporal namespace `default` before starting application workers. Keep credentials in ignored local environment files, with a nonsecret example template.
- [ ] Add `mise run compose:persistence`: complete a run, restart Temporal and PostgreSQL without removing the named volume, and assert the same workflow/run ID and result remain queryable. Prove schema setup is safe to rerun and reset removes old history.
- [ ] Extend local validation with Compose config checks and add PR CI for both image builds, Compose smoke, and persistence using the same local tasks.
- [ ] Run `go test -race ./...`, `docker compose -f deploy/compose.yaml config --quiet`, `mise run compose:up`, and `mise run compose:smoke`; require a completed diamond run. Confirm `compose:down` preserves history and `compose:reset` removes it.
- [ ] Document Compose quick start, reset semantics, and image architecture support. Commit: `feat: run Temporal demo locally with Compose`.

**Definition of done:** A local PostgreSQL-backed Compose diamond completes, history survives Temporal/PostgreSQL restarts, reset removes it, and both images build.

**Local verification:** With Docker running: `mise run validate`, `mise run compose:up`, `mise run compose:smoke`, `mise run compose:persistence`, then `mise run compose:down` succeed. The persistence task checks destructive reset in an isolated test project/volume, not the user's normal data.

**CI increment:** Add PR image builds, Compose validation, bounded smoke, and persistence checks; always clean up CI volumes afterward.

- [ ] **PR:** Branch `feat/compose-postgresql` from updated main after prerequisites merge; open a dedicated implementation PR with the above local evidence and passing cumulative CI. Request review; do not auto-merge.

## Task 5: Rendered application graph validation

**Files:** Create `cmd/validate-graph/main.go`, `internal/manifest/registrations.go`, tests and `internal/manifest/testdata/`; modify `mise.toml`.

**Interfaces:** `manifest.Registrations(r io.Reader) ([]plan.Registration, error)` reads the complete multi-document deployment set, including Namespace resources, selects Namespaces labeled `temporal-demo.io/worker-namespace: "true"`, and extracts annotated Services from them. `go run ./cmd/validate-graph < rendered.yaml` exits nonzero on invalid input and prints plan hash on success. Expand Kubernetes `List` items; require explicit Service namespaces and corresponding Namespace declarations. Reject duplicate identities across namespaces and more than one registered worker Service per namespace. Add `mise run test:manifests` and include it in local validation/PR CI.

- [ ] Write fixtures for valid diamond, roots, unknown dependency, two-node/long cycle, duplicate Service, nonworker resources, malformed YAML, `List`, and namespace filtering. Compare normalized dependencies with Task 1 cases.
- [ ] Run `go test ./internal/manifest`; verify failures before implementing.
- [ ] Implement extraction and reuse `plan.Build`; do not implement another cycle algorithm. Validate the full deployment set, not changed files only.
- [ ] Run `go test -race ./internal/manifest` and CLI commands against valid/invalid fixtures; verify exit codes 0/nonzero respectively.
- [ ] Commit: `feat: validate complete worker graphs before deployment`.

**Definition of done:** Complete rendered manifests yield the same graph/hash as static registration; namespace scope, ambiguous identities, and malformed/cyclic graphs are rejected.

**Local verification:** `mise run validate` and `mise run test:manifests` pass locally without Kubernetes. Invalid fixtures exit nonzero, the valid cross-namespace diamond exits 0.

**CI increment:** Extend automatic validation with complete-manifest fixtures, including Namespace definitions and duplicate identities across namespaces.

- [ ] **PR:** Branch `feat/manifest-validation` from updated main after prerequisites merge; open a dedicated implementation PR with the above local evidence and passing cumulative CI. Request review; do not auto-merge.

## Task 6: kind baseline with fixed worker replicas

**Files:** Create `k8s/kind/config.yaml`, `k8s/infrastructure/{versions.env,temporal.yaml,postgresql.yaml,schema-job.yaml,keda-values.yaml,kyverno-values.yaml}`, `k8s/apps/{Chart.yaml,values.yaml,values-static.yaml}`, `k8s/apps/templates/{namespaces,orchestrator,workers,static-registry}.yaml`, `scripts/{cluster-create,infrastructure-install,images-load,apps-deploy,cluster-delete,validate-manifests}.sh`, `.github/workflows/kind-integration.yaml`; modify `mise.toml`, `docs/versions.md`, `README.md`.

**Interfaces:** Produces `mise run cluster:create`, `infra:install`, `images:load`, `apps:deploy-static`, `api:forward`, `ui:forward`, `cluster:delete`. Static overlay has workers at one replica and no ScaledObjects. Chart values define identities/dependencies once and create annotations plus matching queue environment values. Temporal frontend address is fixed through the chosen Service name in namespace `temporal`.

- [ ] Add render checks asserting each worker Service/Deployment queue identity agrees, application namespaces are explicit, and no ScaledObjects exist in the static overlay. Render to a temporary path and pass it to Task 5 validator; fail before templates exist.
- [ ] Implement single-node kind creation with pinned node image, Helm-pinned controllers, PostgreSQL StatefulSet/Service/PVC, schema initialization Job, and single-instance self-hosted Temporal. Wait for PostgreSQL, successful schema setup, server health, and Temporal namespace bootstrap in that order. Wait for CRDs/controllers/webhooks before their dependent resources. Scripts must verify context `kind-temporal-demo` before mutations or deletion.
- [ ] Render one namespace per worker and a separate orchestrator namespace. Enforce exactly one worker per labeled namespace in the chart/render tests. Use fully qualified cross-namespace Temporal DNS. Add `mise run validate:manifests` using pinned Kubernetes/CRD schemas without skipping unknown kinds, `mise run test:kind-baseline`, and `mise run test:kind-persistence`. The persistence test replaces Temporal and PostgreSQL Pods without deleting PVCs and verifies completed history plus safe schema-job rerun.
- [ ] Introduce bounded PR-triggered kind CI running the same local render, baseline, and persistence checks, with diagnostics and unconditional cleanup. Add Helm lint/render/schema checks to cumulative `mise run validate`.
- [ ] Build/load images and deploy the static registry overlay. Wire health probes, resource requests/limits, termination grace, and one orchestrator replica. Avoid blanket readiness waits against zero-replica Deployments in later overlays.
- [ ] Run all render checks, then `mise run cluster:create`, `infra:install`, `images:load`, and `apps:deploy-static`. Port-forward and run the existing API smoke assertions against kind; require completion without discovery/scaling enabled.
- [ ] Record exact tested versions and host architecture. Commit: `feat: deploy fixed-replica demo to kind`.

**Definition of done:** The baseline diamond runs in kind across five separate application namespaces, with workers fixed at one; PostgreSQL PVC preserves history across server/database Pod replacement.

**Local verification:** With Docker running: `mise run cluster:create`, `mise run infra:install`, `mise run images:load`, `mise run apps:deploy-static`, `mise run validate`, `mise run test:kind-baseline`, and `mise run test:kind-persistence` pass. Integration tasks manage localhost port-forwarding and cleanup; no manual persistent tunnel is assumed.

**CI increment:** Add automatic bounded kind baseline/persistence jobs and local Helm/schema checks. Retain Compose and Go coverage.

- [ ] **PR:** Branch `feat/kind-baseline` from updated main after prerequisites merge; open a dedicated implementation PR with the above local evidence and passing cumulative CI. Request review; do not auto-merge.

## Task 7: Isolated Temporal-to-KEDA scaling experiment

**Files:** Create `k8s/apps/templates/scaledobjects.yaml`, `k8s/apps/values-scaling.yaml`, `tests/integration/scaling_test.go`, `docs/autoscaling.md`; modify worker template and `mise.toml`.

**Interfaces:** Produces `mise run apps:deploy-scaling` and `mise run test:scaling`. Static registry remains enabled so discovery cannot obscure scaler failures. Helm sets worker replicas to zero on first scaled installation; subsequent upgrades omit replica writes once KEDA owns scaling, using an explicit bootstrap value/task rather than a continuous replica reset.

- [ ] Write `TestActivationFromZero` asserting a single registered worker starts at zero, a submitted workflow activates it, the run succeeds, and replicas return to zero within a bounded window. Add rendered-manifest tests for one ScaledObject per worker, min/max 0/1, thresholds 0/1, queue types, cooldown/polling, and no additional HPA.
- [ ] Run `mise run test:scaling` against the baseline; expect activation setup/assertions to fail before scaling manifests exist.
- [ ] Implement the scaling overlay, using the pinned native scaler schema. Run an initial one-worker graph before the diamond. Ensure scaler reaches Temporal while workers are at zero and reports errors distinctly from inactivity.
- [ ] Add `TestInFlightScaleDown` with an activity longer than cooldown and matching test-only timeouts, and `TestDelayedRetryReactivation`. Record whether the baseline interrupts work, then assert eventual bounded recovery rather than uninterrupted execution. Inspect shutdown logs and retry attempts.
- [ ] Evaluate running-workflow-count support against the pinned release; if available, test it as an opt-in values flag, not a safety guarantee. Do not require unsupported metadata.
- [ ] Run render tests and `mise run test:scaling`; require successful activation/completion/return to zero and recorded interruption observations. Commit: `feat: scale Temporal workers from zero with KEDA`.

**Definition of done:** Each ScaledObject targets a worker in its own namespace; activation 0→1, completion, and return to zero are proven. In-flight interruption and delayed retry observations are recorded honestly.

**Local verification:** On Task 6's local cluster: `mise run apps:deploy-scaling`, `mise run validate`, and `mise run test:scaling` pass. The test task installs isolated test graphs/values and restores the baseline.

**CI increment:** Extend automatic kind CI with activation/completion/scale-down and delayed retry checks. Long in-flight experiments may be manually dispatched but must also pass locally before review.

- [ ] **PR:** Branch `feat/keda-scaling` from updated main after prerequisites merge; open a dedicated implementation PR with the above local evidence and passing cumulative CI. Request review; do not auto-merge.

## Task 8: Kyverno admission and registration lifecycle

**Files:** Create `k8s/policies/{validate-workers,generate-registrations,rbac}.yaml`, `k8s/policies/tests/`, `tests/integration/discovery_policy_test.go`, `docs/discovery.md`; add cleanup policy files only if the verified lifecycle needs them; modify `mise.toml`.

**Interfaces:** Produces schema-1 ConfigMaps exactly as specified, plus `mise run policies:test`, `policies:install`, and `test:policies-integration`. Uses shared parser cases from Task 1 as policy fixtures; dependencies need not exist during admission.

- [ ] Write CREATE/UPDATE fixtures for absent/empty dependencies, trimmed valid names, duplicates, empty entries, invalid names, self-reference with whitespace, and annotation removal/false. Require syntax/self-reference rejection on the Service and allow unrelated resources. Run `mise run policies:test`; expect missing policies to fail.
- [ ] Implement enforced validation in namespaces labeled `temporal-demo.io/worker-namespace: "true"`. Validate `temporal-worker` values when present, but allow missing/false to deregister. Scope dependency-list checks to true registrations. Use the pinned Kyverno release's supported expression syntax; prove whitespace handling with actual policy tests, not pseudocode.
- [ ] Implement synchronized generation and background reconciliation for preexisting Services. Grant generation permissions only as needed; generated data derives identity, queue, workflow type, dependencies, source namespace, and source UID from the Service. Keep the ConfigMap in that worker namespace; test identical ConfigMap names in distinct namespaces without collisions. A second globally identical worker identity must invalidate the combined graph. Policy generation does not grant the orchestrator access by itself.
- [ ] Run lifecycle integration tests for create, update, delete, disable, annotation removal, delete/recreate with a new UID, and policy installation after the source exists. Record which cleanup events native generation handles. If needed, implement and test a narrowly scoped cleanup policy; if the pinned release cannot meet deletion/removal semantics without source validation, stop for a documented design amendment before Task 9. Do not conceal stale registration behind orchestrator filtering alone.
- [ ] Verify background reporting of preexisting violations and that ordinary Pods/Deployments do not generate entries. Run offline tests plus `mise run test:policies-integration`; require expected ConfigMap convergence with bounded waits.
- [ ] Commit: `feat: discover workers with Kyverno registration policies`.

**Definition of done:** Admission rules and ConfigMap generation work in every labeled worker namespace; source create/update/delete/disable/removal and preexisting-resource reconciliation meet the lifecycle contract.

**Local verification:** On the local kind stack: `mise run policies:test`, `mise run policies:install`, `mise run test:policies-integration`, and `mise run validate` pass. Tests use temporary labeled namespaces and remove them afterward.

**CI increment:** Add offline policy checks to validate and lifecycle tests to automatic kind CI, retaining scaling coverage.

- [ ] **PR:** Branch `feat/kyverno-discovery` from updated main after prerequisites merge; open a dedicated implementation PR with the above local evidence and passing cumulative CI. Request review; do not auto-merge.

## Task 9: Live Kubernetes registry and pre-start protection

**Files:** Create `internal/registry/{kubernetes,decode}.go`, `internal/registry/kubernetes_test.go`, `k8s/apps/templates/rbac.yaml`, `k8s/apps/values.yaml` discovery settings, `tests/integration/registry_test.go`; modify orchestrator wiring and chart.

**Interfaces:** `registry.NewKubernetes(client kubernetes.Interface, namespaceSelector string) *Kubernetes`; `(*Kubernetes).Run(ctx context.Context) error`; implements Task 1 `Provider`. Watch Namespaces matching `temporal-demo.io/worker-namespace=true`, then create one namespace-scoped registration watcher per selected namespace. A ClusterRole grants only Namespace get/list/watch. Each worker namespace has a ConfigMap read-only Role and a RoleBinding whose subject is the orchestrator service account in `temporal-demo-orchestrator`; no cluster-wide ConfigMap permission. All selected namespace caches must synchronize before accepting starts. Adding a namespace activates a watcher without restarting the orchestrator; deleting it or removing its label removes its registrations. Failed namespace reads invalidate synchronization rather than silently dropping workers.

- [ ] Write fake-client/watch tests for initial unsynchronized state, add/update/delete, malformed ConfigMap, unknown schema, duplicate worker identity across namespaces, mutation isolation, expired resource version/relist, and canceled watch. Include dynamically added/removed/unlabeled Namespaces, a selected namespace with missing RoleBinding, recovery after access is granted, and blocking synchronization until all selected namespace caches are ready. Test invalid graph snapshots produce diagnostics rather than retaining a previously valid plan.
- [ ] Define and test watch-health behavior: a transient restart retains cache only while the client is successfully recovering; after 30 seconds without a successful list/watch connection, mark the registry unsynchronized and reject new runs until recovery. An idle healthy watch must not become stale merely because no resource changed.
- [ ] Run `go test ./internal/registry`; expect failures before implementation. Implement decoding and informer-driven immutable publication; expose readiness and diagnostics through the existing handlers.
- [ ] Add integration assertions for partial reconciliation, multi-Service updates that introduce a cycle, removal, and restart/relist. Assert invalid observed state returns 503 and creates no Temporal execution; a valid but not-yet-complete intended rollout cannot be inferred by the service, so deployment automation must wait for its expected registry/hash.
- [ ] Switch the full chart from static registry to Kubernetes mode after policy readiness. Run unit tests with `-race`, `mise run test:registry`, and `mise run test:rbac`. Assert the orchestrator can read Namespaces and ConfigMaps in each worker namespace but cannot read ConfigMaps in unrelated namespaces or Secrets anywhere, nor write resources. Verify it still runs only in its own namespace.
- [ ] Commit: `feat: build run plans from live worker registrations`.

**Definition of done:** The orchestrator combines dynamically discovered namespace registries, rejects unsynchronized/invalid graphs before starting workflows, and has only the intended cross-namespace access.

**Local verification:** With Tasks 6–8 installed: `mise run apps:deploy-discovery` (created in this task), `mise run validate`, `mise run test:registry`, and `mise run test:rbac` pass, including namespace add/remove, denied access, and recovery cases.

**CI increment:** Extend automatic kind CI with registry lifecycle, invalid-graph rejection, and RBAC checks. Keep all earlier jobs.

- [ ] **PR:** Branch `feat/live-registry` from updated main after prerequisites merge; open a dedicated implementation PR with the above local evidence and passing cumulative CI. Request review; do not auto-merge.

## Task 10: Full acceptance and recovery experiments

**Files:** Create `tests/integration/{diamond,resilience}_test.go`, `scripts/{smoke,collect-diagnostics}.sh`, `docs/experiments.md`; modify `mise.toml`.

**Interfaces:** Produces `mise run smoke` and `mise run test:resilience`. Integration tests read `DEMO_API_URL`, `TEMPORAL_ADDRESS`, and explicit Kubernetes context. Collect workflow histories, Pod events/logs, ScaledObject conditions, and registry snapshots on failure, excluding secrets.

- [ ] Write `TestDiamondFromZero` that waits for exactly four expected registrations and the canonical expected hash, verifies all child workers are zero, starts a run, and checks parent history: B/C starts follow A completion; D starts follow both B/C completions; each child runs once with the expected queue. Verify result retrieval and eventual zero replicas with a 10-minute overall smoke deadline.
- [ ] Write `TestPlanSnapshotIsolation`: update dependencies while one run is active, show that its input/hash and execution remain unchanged, then assert a new run uses the new plan.
- [ ] Write recovery tests: terminate worker during activity, restart orchestrator during execution, exercise retry after worker scale-down, and temporarily interrupt Temporal connectivity without deleting its pod/storage. Assert retry attempts are observable, API errors are truthful, and recovery completes within configured execution bounds. Restore baseline resources after each experiment.
- [ ] Run tests before any remaining glue changes; fix missing wiring without weakening ordering or lifecycle assertions. Use Temporal histories/results, not log timing, for ordering assertions.
- [ ] Run `mise run smoke` twice, including after a fresh cluster recreation, and `mise run test:resilience`. Record commands, versions, outcomes, measured activation/scale-down times, and known limitations; never fill observations with unmeasured estimates.
- [ ] Commit: `test: verify discovery orchestration and scaling end to end`.

**Definition of done:** All design acceptance scenarios pass across separate namespaces; recovery attempts and measured scaling timings are documented, and existing runs retain their original plan.

**Local verification:** On a local fully deployed kind stack: `mise run validate`, `mise run smoke`, and `mise run test:resilience` pass. Recreate the cluster and repeat smoke. Integration tasks supply bounded port-forwarding, fixtures, and diagnostic collection.

**CI increment:** Add the full diamond smoke to automatic PR CI; expose the longer resilience suite via manual dispatch using the same local task, without removing earlier regression tests.

- [ ] **PR:** Branch `test/full-acceptance` from updated main after prerequisites merge; open a dedicated implementation PR with the above local evidence and passing cumulative CI. Request review; do not auto-merge.

## Task 11: Operator documentation and final acceptance

**Files:** Create `docs/architecture.md`; finalize `README.md`, `docs/{api,versions,discovery,autoscaling,experiments}.md`, `mise.toml`; audit existing `.github/workflows/` and `scripts/validate-manifests.sh` without deferring their creation to this task.

**Interfaces:** `mise run validate` runs formatting checks, Go tests/race/vet, Helm lint/render, complete graph validation, Compose config validation, and Kyverno policy tests. `mise run validate:manifests` validates built-in resources and custom resources against pinned schemas without silently skipping unknown CRDs. The bounded kind suite already runs on PRs; long resilience cases additionally support `workflow_dispatch`. All commands are runnable locally.

- [ ] Audit cumulative CI introduced in Tasks 1–10: all prior tests remain active, local commands match CI commands, actions are pinned, permissions are minimal, time budgets are explicit, and failure diagnostics/cleanup work. Document verified Linux amd64 and local ARM64 coverage separately; do not claim untested platforms.
- [ ] Write README prerequisites, Compose path, kind path, API trigger/status examples, UI forwarding, reset/teardown warnings, and expected startup latency. Explain PostgreSQL-backed history across Pod/container restarts, destructive volume/PVC/cluster reset behavior, schema initialization, separate namespaces and cross-namespace RBAC, eventual registration, plan snapshots, trusted registration sources, retries, and scale-down limitations.
- [ ] Run `mise run validate`, image builds, manifest validation, policy lifecycle tests, full smoke, and resilience tests. Record any unavailable environment or skipped test explicitly; no claim of validation for commands not run.
- [ ] Reconcile all fourteen design acceptance criteria with integration-test names and documented manual experiments. Commit: `docs: finalize reproducible workflows and acceptance evidence`.

**Definition of done:** A fresh local checkout can follow README to reproduce Compose and kind workflows; every acceptance criterion maps to evidence, and cumulative CI has no coverage regressions.

**Local verification:** From clean local environments, run the documented Compose and kind quick starts, `mise run validate`, `mise run compose:smoke`, `mise run compose:persistence`, `mise run validate:manifests`, `mise run test:policies-integration`, `mise run smoke`, and `mise run test:resilience`; all required checks must pass. Confirm documented teardown/reset behavior.

**CI increment:** Audit and repair existing workflows only; CI was already introduced in Task 1 and extended in each task. Keep prior coverage enabled.

- [ ] **PR:** Branch `docs/final-acceptance` from updated main after prerequisites merge; open a dedicated implementation PR with the above local evidence and passing cumulative CI. Request review; do not auto-merge.

## Completion and execution handoff

Deliver one PR per numbered task, normally sequentially so each branch starts from tested, merged main. Task 5 can follow Task 1 independently, and Task 8's offline policy work can proceed after the version gate, but do not merge integration-dependent tasks before their prerequisites. Do not enable combined discovery/scaling until their isolated tests pass.

The plan has been checked against the design for registration lifecycle, all API endpoints, workflow determinism, cancellation, persistence/reset, version pinning, validation layers, scale-to-zero, CI, and recovery tests. Compatibility remains an explicit implementation gate, not a claim that infrastructure has been tested. No product code or cluster resources are created by this planning change.

Recommended execution: native task-by-task implementation for this small personal project, with review at the Compose, KEDA, and Kyverno milestones. Subagent-driven execution is also suitable if independent review per task is preferred. Obtain plan approval and the execution choice before starting Task 1.
