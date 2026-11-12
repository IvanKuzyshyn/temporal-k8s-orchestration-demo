# Minimal Full Demo Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Support only the full kind/Kyverno/KEDA demo through up, body-free workflow POST, down and destroy, retaining idempotency and replacing graph CLI validation with a read-only API plus offline tests.

**Architecture:** Keep the existing workflow, registry and owned lifecycle boundaries. Replace the graph CLI with shared snapshot validation and `/graph`, make the full chart the only deployment, and replace the staged experiment framework with one disposable lifecycle integration test. Remove obsolete implementations only after their relevant guarantees have replacement coverage.

**Tech Stack:** Go, Temporal Go SDK, client-go, chi, Bash/jq, mise, Helm, kind, KEDA, Kyverno, GitHub Actions. Keep current version pins.

**Spec:** `docs/superpowers/specs/2026-11-11-minimal-demo-design.md` (approved scope and explicitly identified implementation choices).

## Global Constraints

- Runtime commands: `mise run up`, `mise run down`, `mise run destroy`; setup remains `mise install`.
- API `http://127.0.0.1:18080`; UI `http://127.0.0.1:18088`; keep existing port overrides and localhost binding.
- Cluster `temporal-demo`, context `kind-temporal-demo`, checkout-local `.local/kubeconfig`; never adopt or reset another checkout's cluster.
- Keep durable idempotency, `GET /runs/{workflowID}`, health/readiness, Temporal history and PostgreSQL credential/PVC preservation.
- `/graph`: 200 valid, 422 synchronized-invalid, 503 unsynchronized. Invalid graphs block new starts, never durable keyed replay.
- After controllers/policies/API are ready, poll `/graph` every second for at most 120 elapsed seconds; no overlapping requests, each capped at min(2 seconds, remaining budget). Fail with last-outcome and missing/mismatched-worker diagnostics.
- Four-worker diamond, Temporal namespace `default`; KEDA min/max 0/1, polling 5s, cooldown 120s, workflow and activity queues.
- No production static registry, Compose, fixed-worker mode, optional running-count signal, or advanced fault-injection suite in the final tree.
- Remove `cmd/validate-graph`, not CI graph tests or strict Kubernetes resource validation.
- Keep shared `plan.Build`, immutable snapshots, least-privilege discovery RBAC, bounded waits, cancellation and secret-safe diagnostics.
- No automatic upgrade of old installed images; no destructive live testing without first establishing disposable ownership.

## Review Focus

1. A valid partial registry must not satisfy full startup; same worker names with different dependencies must also fail the intended-graph check (Tasks 1–2).
2. Invalid/unsynchronized discovery must not regress keyed replay, concurrency or ambiguity handling (Tasks 1, 6).
3. Interrupted bootstrap/repeated up must not reclaim worker replicas from KEDA or destroy retained data (Tasks 2–3, 5).
4. A failed integration preflight must never clean up a pre-existing cluster, occupied port owner or unrelated process (Tasks 2–3).
5. Deletion must not remove unique guarantees hidden in old tests, leave runtime CLI/static dependencies, or make CI stop testing the actual public commands (Tasks 4–7).

## File ownership and sequencing

- `internal/registry/validation.go`: common validation of a copied snapshot; used by HTTP diagnostics and run admission.
- `services/orchestrator/api/{router,handlers}.go`: graph HTTP contract; run/idempotency protocol stays intact.
- `scripts/expected-workers.jq`: project intended rendered registration resources into a sorted readiness expectation, persisted at `.local/lifecycle/expected-workers.json`; never a runtime registry or a checked-in duplicate graph.
- `scripts/local.sh`: owned lifecycle and HTTP readiness. `scripts/k8s-up.sh`: internal full-only bootstrap. Keep infrastructure/policy/render helpers separate.
- `tests/integration/demo_*_test.go` and `scripts/test-demo.sh`: one disposable lifecycle test and narrow history assertions, no retained-stack rollback machinery.
- Chart defaults/templates/schema: one deployment configuration, initial zero bootstrap and replica-free steady state.
- `mise.toml`, `.github/workflows/`, docs: minimal public interface, nonduplicated validation and CI.

Execute Tasks 1–7 in order. Transitional compatibility is allowed only where explicitly noted; Task 7 checks final absence. Do not mass-delete tests before recording where their retained assertions move.

## Task 1: Shared validation and graph HTTP contract

**Files:** Create `internal/registry/validation.go`, `internal/registry/validation_test.go`, `services/orchestrator/api/graph_test.go`. Modify `services/orchestrator/runs/service.go`, `services/orchestrator/api/router.go`, `services/orchestrator/api/handlers.go` and their tests.

**Interfaces:** Add `registry.ValidateSnapshot(snapshot Snapshot) (plan.Plan, error)` and `registry.ErrInvalidSnapshot`. Reuse the current `validatedCopy` checks: synchronized/no problems, copy nodes/dependencies, rebuild with `plan.Build`, reject noncanonical or mismatched plan/hash. `runs.Service.Start` maps validation failure to existing `runs.ErrInvalidRegistry` without moving validation ahead of keyed durable lookup. Add handler `graph(w http.ResponseWriter, r *http.Request)` returning `{synced bool, valid bool, plan plan.Plan, problems []string}`.

- [ ] **Write failing tests.** Table cases for `/graph`: valid diamond → 200/true/empty problems; unsynced snapshot → 503/false; empty, cycle, unknown dependency, malformed identity, stale hash/noncanonical snapshot → 422/false. Assert GET does not call the Temporal gateway or mutate provider data. `/readyz` remains 200 for synchronized-invalid graphs when its connectivity/polling check passes. ValidateSnapshot copy tests mutate returned dependencies without affecting the provider. Reuse literal expected statuses and identities, not values computed by the implementation under test.
- [ ] **Verify RED.** Run `mise exec -- go test ./internal/registry ./services/orchestrator/api ./services/orchestrator/runs -count=1`; new endpoint/helper cases must fail before implementation.
- [ ] **Implement the shared helper and endpoint.** Move rather than duplicate `validatedCopy`. Preserve the snapshot plan for invalid diagnostics; derive `valid` from the common helper, use a stable validation problem when provider problems are empty, and never return null for `problems`. Unsynchronized status wins over other invalidity. Keep `/workers` temporarily for the old acceptance harness; it is removed in Task 5.
- [ ] **Verify GREEN and retained behavior.** Run the same packages with `-race`. Existing service tests for replay on invalid/unsynchronized registry, lookup failure and duplicate races must remain green.
- [ ] **Commit** these API/shared-validation changes as `feat: expose shared runtime graph validation`.

## Task 2: Use the graph API for complete lifecycle readiness

**Files:** Create `scripts/expected-workers.jq`, `internal/chart/graph_test.go`. Modify `scripts/local.sh`, `scripts/k8s-up.sh`, `internal/chart/local_resume_test.go`, `internal/chart/local_test.go`.

**Interfaces:** Project the intended rendered worker Services/deployments into sorted expected nodes (name, queue, workflow type, normalized dependency set); ignore unregistered Services and use the same namespace selection as discovery. Consume the JSON document stream from `kubectl create --dry-run=client -f <validated-render> -o json` with jq slurp, not an assumed Kubernetes List. Write `.local/lifecycle/expected-workers.json` atomically during successful bootstrap, before marking initialization complete, and retain it across resume. `local.sh up` succeeds only on `/graph` 200, synced/valid true, empty problems and exact expected nodes. Do not compare a startup hash or accept just names/count. Fail closed if retained expectation state is missing or malformed.

- [ ] **Write failing tests.** Render the existing full chart, extract registrations and build the plan in Go, then require the shell projection to equal its nodes; independently assert the canonical four-worker diamond in the test. Cover dependency whitespace/duplicates and ignored Services so projection does not silently diverge from the Go parser. Extend executable lifecycle boundary tests with complete, connection-error then complete, 503 then complete, 422 then complete, valid subset then complete, unexpected extra worker, and right names/wrong dependencies. Prove an immediate first request, retries at one-second intervals when requests are fast, no overlap, immediate exit on success, capped request timeouts/sleeps, and a total 120-second elapsed deadline even with slow or hanging requests. Use test-boundary clock/sleep/HTTP doubles rather than adding production timeout settings solely for tests. Timeout output must include the last status/problems and missing/mismatched names. Add missing/malformed expectation-state refusal.
- [ ] **Verify RED.** Run `mise exec -- go test ./internal/chart -run 'Test(Local|FullLocal|DemoGraph)' -count=1`; old `/workers` polling must fail the new route/completeness/deadline assertions.
- [ ] **Implement the projection and bounded readiness loop.** Begin the graph budget only after infrastructure/policies and API readiness. Use an elapsed-time deadline, not an attempt count; requests get at most two seconds capped to remaining time, and one-second sleeps are also capped. Preserve response bodies for diagnostic HTTP 422/503 rather than losing them through curl fail mode. Do not calculate the plan hash in Bash/Helm, copy API output as the expectation, or feed lifecycle state to discovery. Keep installed-image resume behavior and ownership/port/tunnel checks. The old bootstrap may still write plan-hash temporarily; delete that writer with the CLI in Task 5.
- [ ] **Verify GREEN.** Run the focused command with `-race`, including occupied-port, reused-PID, delayed-nohup-exec, foreign/replaced-container and repeated-up cases.
- [ ] **Commit** as `refactor: gate local startup on the complete graph API`.

## Task 3: Establish one integration test of the supported lifecycle

**Files:** Create `scripts/test-demo.sh`, `tests/integration/demo_lifecycle_test.go`, `tests/integration/demo_history_test.go`, `internal/chart/demo_runner_test.go`. Add developer task `test:integration` in `mise.toml`. Read/reuse relevant assertions from `tests/integration/diamond_test.go` and `acceptance_assertions_test.go`, not their deployment/preflight/cleanup harness.

**Interfaces:** `test-demo.sh` owns disposable setup/cleanup and runs `mise run up`, then `go test -tags=integration -count=1 -run '^TestDemoLifecycle$' -v -timeout=20m ./tests/integration`. Test gets root/API address through `DEMO_ROOT` and `DEMO_API_URL`; use a test-owned Temporal port-forward only for SDK history inspection. New helper names use a `demo` prefix to avoid coupling/collisions with the legacy harness until its deletion. The test invokes `mise run down/up` for resume checks; the runner owns final destroy and cleanup. No SCALING_*, TASK10_*, retained-history witness or global cluster reset prerequisites.

- [ ] **Write failing runner safety tests.** Preexisting running/stopped node, unowned/replaced container, existing ownership state, Docker discovery failure and lifecycle lock cause refusal before setup/destruction. A test failure after successful owned creation must run diagnostics then destroy only that created node. Cleanup after failed kind creation must not adopt/delete an unrecorded node. Exercise the real runner with external command boundaries replaced, not source-string assertions.
- [ ] **Verify RED.** Run `mise exec -- go test ./internal/chart -run TestDemoRunner -count=1`; missing runner/new behavior fails.
- [ ] **Implement the disposable runner.** Refuse any existing named cluster or stale local ownership before mutation. Do not replace arbitrary kubeconfigs. On failure collect safe diagnostics while the node is still available; preserve the primary error and cleanup errors. Never run the integration test against a user's retained cluster. Use the existing lifecycle ownership contract rather than inventing an execution cleanup ledger.
- [ ] **Write the lifecycle test and narrow history-sensitivity tests.** Prove generated ConfigMaps reference the actual Services and expected worker metadata; essential discovery RBAC permits required reads and denies Secrets/writes; four healthy scalers and zero actual worker Pods; body-free unkeyed POST → 202/identifiers; dependency-ordered child histories and identity-only result; workers return to zero. Then create one keyed run, complete and replay it, record PVC/credential identities, run down twice, assert node stopped and tunnels unavailable, resume up, verify identities and byte-equivalent keyed result, and complete a new unkeyed run with a distinct ID. Verify repeated up does not reset idle replicas. A malformed-body POST remains rejected. Test helper sensitivity must reject missing child, wrong queue, wrong dependency order and incorrect payloads.
- [ ] **Verify the real scenario.** On a confirmed unused Docker daemon/checkout, run `mise run test:integration`. Expect successful full lifecycle and removal of its cluster; run destroy a second time to prove idempotence. If the named cluster is already present, report the preflight refusal and request an isolated environment instead of deleting it. No live test claim without actual evidence.
- [ ] **Commit** as `test: cover the supported full demo lifecycle end to end`.

## Task 4: Delete alternate runtime and experiment infrastructure

**Files to remove:**
- `deploy/compose.yaml`, `deploy/.env.example`, `deploy/temporal/`, `cmd/healthcheck/`.
- `scripts/compose.sh`, `compose-common.sh`, `compose-smoke.sh`, `compose-persistence.sh`, `compose-cleanup.sh`.
- `scripts/scaling.sh`, `discovery.sh`, `smoke.sh`, `kind-integration.sh`, `collect-diagnostics.sh` (keep `kind-diagnostics.sh` as the retained diagnostic implementation).
- `.github/workflows/compose.yaml`, `internal/composetest/`.
- Old `tests/integration/{scaling,scaler_errors,activation_convergence,registry,discovery_policy,resilience,diamond,retained_history,start_ownership,cleanup,acceptance_assertions}_test.go` after migrating Task 3's named assertions. Remove `relay.Dockerfile` and `relay.Dockerfile.dockerignore`.
- `internal/chart/{scaling_safety,discovery_safety,discovery_workflow,acceptance_shell,workflow}_test.go` once their obsolete entrypoints disappear.
- `internal/registry/selectors_test.go`: despite its location, this tests the old experiment build-tag/selector matrix, not registry correctness. Replace it with a small integration-test discovery check only if needed; do not preserve the old tag matrix.

**Files to modify:** Both app Dockerfiles, `internal/chart/infrastructure_test.go`, `mise.toml`, `.github/workflows/kind-integration.yaml`; documentation links made dangling by this task.

**Interfaces:** Dockerfiles still build app binaries and expose HTTP probes, but no healthcheck executable. Kubernetes `temporal.yaml` becomes the sole runtime source for its embedded bootstrap scripts/configuration. The existing static JSON file/provider remain until Task 5 to keep transitional unit tests compiling.

- [ ] **Record a coverage-transfer checklist before deletion.** Identify each retained assertion from removed files and its new home: graph/order/results → demo history tests; scale-to-zero → lifecycle integration; scoped RBAC/registrations → lifecycle integration plus policy unit tests; state preservation → down/up integration; HTTP body/idempotency → API/service tests. Unique adversarial discovery race tests in `internal/registry` are not deleted.
- [ ] **Add/retain infrastructure regression tests.** Preserve schema/namespace Job ordering and bounded retry, immutable Secret/PVC handling and safe schema diagnostics. Replace Compose-copy-parity assertions with assertions against the actual Kubernetes configuration. Keep policy CREATE/UPDATE/DELETE and generated registration schema tests under `k8s/policies/tests`.
- [ ] **Remove obsolete code and its callers together.** Remove compose/staged/resilience task entries and obsolete build-tagged test invocations from validate. Replace old kind CI steps immediately with the Task 3 runner so CI never references deleted commands; final deduplication is Task 7. Keep static/default bootstrap tasks temporarily only until Task 5. Do not delete `deploy/config/workers.json` or its Docker COPY yet.
- [ ] **Verify.** Run `mise exec -- go test -race ./...`, `mise exec -- go vet ./...`, and Bash syntax checks for all retained scripts. Build both Dockerfiles; expect only app binary build, no missing `cmd/healthcheck`. Run `mise run test:integration` only on the disposable environment. Check removed script/task names have no executable callers; remaining historical documentation references are handled in Task 7.
- [ ] **Commit** as `refactor: remove compose and staged acceptance experiments`.

## Task 5: Make the full deployment unconditional; remove static registry and graph CLI

**Remove:** `internal/registry/static.go`; static-only test cases; `deploy/config/workers.json`; `cmd/validate-graph/`; chart `values-static.yaml`, `values-discovery.yaml`, `values-scaling.yaml`, `values-local.yaml`, `templates/static-registry.yaml`; `scripts/k8s-down.sh`, `scripts/kind-validation.sh`, `k8s/server-validation/`; old `internal/chart/context_test.go` and dedicated standalone server-validation fixture tests superseded below.

**Modify:** `internal/runtimeconfig/config.go` and tests; `internal/process/process.go`, `lifecycle_test.go`, `discovery_test.go`; both Dockerfiles as needed; `internal/plan/types.go`, `internal/registry/registry.go` comments; `internal/manifest/registrations_test.go`; all retained chart tests; chart defaults/schema/templates; `scripts/{local,k8s-up,validate-manifests,validate-k8s}.sh`; `mise.toml`.

**Interfaces:** `runtimeconfig.Config` no longer has RegistryMode/RegistryFile; orchestrator always constructs `registry.NewKubernetes`. Preserve namespace selection and per-worker identity configuration. Activity duration is fixed at the five-second deployed default rather than an environment-level experiment knob; internal unit-test injection may remain. `k8s-up.sh` is internal full-only bootstrap with no mode argument. `validate-k8s.sh` validates a supplied render only. `validate-manifests.sh` lints/renders the one supported chart and verifies pinned controller rendering; graph semantic checks are Go tests. `local.sh` no longer calls the graph CLI or reads generated plan-hash state.

- [ ] **Write failing single-mode tests.** Update chart schema cases to reject removed `registry.mode`, `scaling.enabled`, fixed worker `replicas` and optional running-count values. Render default chart (no overlays), assert the rendered registrations form the canonical diamond and the Task 2 readiness projection equals the built nodes; assert RBAC, queue identities, scaler metadata, Pod security/probes and no static ConfigMap. Test bootstrap true gives four zero worker replica specs; steady render omits worker replicas and keeps orchestrator at one. Use YAML parsing, not substring/count-only assertions.
- [ ] **Verify RED.** Run `mise exec -- go test ./internal/chart -count=1`; defaults still being static and accepting removed options must fail the new contract tests.
- [ ] **Consolidate the chart.** Move full settings to values.yaml, remove switches/unused schema properties/overlays and make RBAC/scalers unconditional. Keep `bootstrapZero` and configurable native polling/cooldown values only where they represent the one supported scaling policy, not optional experiments. Remove static mounts/checksum and registry mode env. Keep all worker identity/dependency settings consistent through values and policy-generated registrations.
- [ ] **Remove production static loading.** Update process construction and tests to use the existing fake Kubernetes boundary. Move `TestSnapshotDeepCopy` into registry snapshot tests; delete only static-loader parsing cases. Replace static JSON expectations in manifest/chart tests with independent in-test diamond expectations and direct registrations. Remove the Dockerfile static COPY and `COPY cmd ./cmd` once both command binaries are gone. Preserve live discovery watch/relist/authorization tests unchanged unless signature updates require mechanical edits.
- [ ] **Simplify bootstrap and delete the graph CLI.** Remove baseline branch and worker-one rollout assumptions, mode args, all `go run ./cmd/validate-graph` calls and plan-hash writes. Keep strict dry-run/apply of the same manifest bytes and zero-bootstrap/last-applied handoff, including interrupted retry annotation cleanup. Adapt executable lifecycle tests to the single mode; retain schema/controller/validation failures and replica-ownership cases. No CLI replacement under another name. Drop standalone server-validation exercises while retaining a test that server rejection prevents real apply.
- [ ] **Remove transitional API and task aliases.** Delete `/workers` and update retained API/process tests to `/graph`/new status behavior; assert `/workers` returns 404. Remove k8s:up/down, api/ui:forward and public policy-install/standalone validation entrypoints. Keep their necessary helper implementation private to up/validate. Only the three demo lifecycle commands plus developer test/validate/test:integration remain in mise.
- [ ] **Verify GREEN.** Run `mise exec -- go test -race ./...`, `mise exec -- go vet ./...`, both Docker builds and `mise run test:integration` in the disposable environment. Search executable/config sources for `cmd/validate-graph`, `LoadStatic`, `REGISTRY_MODE`, `REGISTRY_FILE`, `/workers`, deleted overlays and Compose commands; expect no remaining runtime dependency. Lifecycle expectation-state reading must not appear in process/workflow execution code.
- [ ] **Commit** as `refactor: keep only live discovery and native scale-to-zero`.

## Task 6: Consolidate tests without weakening idempotency

**Files:** `services/orchestrator/{api/router_test.go,api/graph_test.go,runs/service_test.go,runs/temporal_test.go}`; consolidated chart tests; `tests/integration/demo_lifecycle_test.go` only if a missing real guarantee is identified.

**Interfaces:** No production idempotency semantics change. `Start(ctx,key)` still checks the durable execution before validating the current graph. Test ownership is explicit by layer, not by historical feature phase.

- [ ] **Write a coverage map using existing test names before deleting any case.** Service owns deterministic/unique IDs, duplicate race with running/completed original, original hash after valid/invalid registry changes, replay after new service construction, ambiguous accepted start, failed lookup/no start, malformed durable metadata and input-copy isolation. API owns key/body/header parsing, JSON/status/redaction, deadlines and graph status contract. Gateway owns SDK start policies, Memo decode, pinned described run result and SDK error mapping. Real integration owns one durable replay across actual node stop/resume plus unkeyed execution.
- [ ] **Add any missing regression before consolidation.** In particular prove `/graph` 422 and 503 do not block an existing keyed POST, but reject a new key with 503. If already covered, reuse it rather than manufacturing a redundant RED step. Any genuinely new behavior case must fail before its fix.
- [ ] **Remove only repeated orchestration from HTTP tests.** Replace the full concurrency/restart loop in `TestHTTPConcurrentDuplicatesAndCompletedRetryAfterRestart` with a small transport replay/serialization check; service race tests remain. Reduce `TestHTTPDurableRepeatAndRegistryChange` and `TestHTTPAmbiguousStartAndUnavailableLookup` to unique HTTP assertions, leaving detailed service cases intact. Keep invalid key multiplicity, byte/whitespace/chunked bodies, safe 503 ambiguity and metadata failure coverage.
- [ ] **Consolidate chart tests.** Fold useful assertions from old Static/Discovery/Scaling/FullLocal render tests into one full-chart suite with bootstrap/steady subtests; retain schema/security/RBAC checks and independent expected-plan assertions. Remove source-text tests that merely assert obsolete CI step names. Keep executable safety tests and distinct live-registry race tests.
- [ ] **Verify.** Run `mise exec -- go test -race ./services/orchestrator/... ./internal/chart ./internal/registry -count=1`, then the full race suite. Compare the coverage map before/after; every retained guarantee must name an actual surviving test. Report scenario removals, not just reduced file/line counts.
- [ ] **Commit** as `test: consolidate graph and idempotency coverage`.

## Task 7: Finish CI, documentation and acceptance

**Files:** `mise.toml`, `.github/workflows/validate.yaml`, `.github/workflows/kind-integration.yaml`, `README.md`, `docs/{api,architecture,autoscaling,discovery,versions}.md`, `k8s/infrastructure/README.md`. Remove `docs/experiments.md` if its useful caveats have moved. Mark old superseded specs/plans as historical rather than advertising their commands as supported.

**Interfaces:** Final tasks: up/down/destroy plus developer `test` (race suite), `validate` (format/race/vet/policy/chart/shell checks) and `test:integration` (disposable real lifecycle). One validation CI job and one integration CI job invoke these tasks without a duplicated full validation pass. Policy execution appears once per validate invocation, not again after an all-package test that already includes it.

- [ ] **Simplify validate and CI.** Update `internal/chart/shell_test.go` to exercise the retained validate task without assuming Compose scripts exist. Remove redundant reruns of manifest/policy packages, old integration/resilience tags, Compose config and optional long-experiment dispatch paths. Keep real Helm/schema/graph and policy checks. CI integration calls the supported lifecycle runner; always gather secret-safe failure diagnostics before owned cleanup. Keep pinned action versions and finite job/test timeouts. Never upload kubeconfigs, Secrets, credential files or unfiltered `.local/` directories.
- [ ] **Rewrite user docs.** Lead with install/up/curl/down/destroy, UI and k9s connection, worker namespaces, zero-worker expectations, key reuse and retention, GET /graph status meanings, and GET /runs. Explain Kyverno registration versus KEDA deployment scaling. Document the one-second/120-second graph convergence wait, timeout diagnostics, deployment-derived expectations, fixed activity duration, down interruption, source-code changes requiring explicit recreation, old-image upgrade boundary, occupied ports and ownership refusal. Remove staged instructions and outdated claims of CLI predeployment graph validation.
- [ ] **Run final fast validation.** `mise run validate` and `git diff --check` must pass. Review the task list and repository references for deleted functionality. Offline tests must reject malformed rendered graphs and startup projections with missing workers or wrong dependencies; test mutations must remain in temporary fixtures.
- [ ] **Run final real acceptance.** On disposable Linux CI, `mise run test:integration` must prove unkeyed workflow, zero→activation→zero, keyed replay, down/up storage identity and repeated destroy. Repeat locally on macOS ARM64 if available; report actual platforms, not inferred coverage. Confirm unrelated Docker containers and the user's normal checkout are untouched.
- [ ] **Review whole diff against the spec and coverage map.** Use a fresh reviewer if tools are available; otherwise explicitly record self-review. Check the five Review Focus items. No large abstraction rewrite or newly added demo mode should be hidden in cleanup.
- [ ] **Commit** as `docs: document and validate the single supported demo`. Prepare PR summary with removed features, preserved contracts, verification evidence and explicit migration caveat. Push/open a PR only when requested for execution; this planning task does not authorize publication.

## Plan self-review

- Scope covered: removals 1–4, duplicate Temporal inputs, public command reduction, test consolidation, idempotency retained, graph CLI removed while CI tests remain.
- Startup derives expected registration nodes from deployment resources, polls the live API every second with a 120-second deadline, and has no checked-in duplicate graph, startup hash calculation or execution fallback.
- Transitional dependencies are retired in order: endpoint added → local gate updated → real replacement test → experiment removal → static/CLI/mode removal → deduplication → final CI/docs.
- Existing stored keyed executions retain their Memo/hash protocol; no redefinition of workflow IDs or serialized workflow plans.
- Destructive testing is confined to newly created disposable resources and does not rely on an ordinary developer cluster being empty.
- Plan created only; no implementation or verification claims are implied by unchecked steps.
