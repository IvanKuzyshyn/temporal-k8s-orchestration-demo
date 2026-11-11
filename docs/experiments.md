# Acceptance, recovery and evidence

Use the [README quick starts](../README.md) for the operational path. This page
explains what each proof establishes, where it ran, and what remains unverified.
All mutating suites run sequentially on the dedicated owned kind cluster, with
no unrelated active executions. Never fix a failed ordinary experiment by
resetting storage, rotating credentials or broad workflow cleanup.

## Reproduce the combined tests

Prerequisites: healthy PostgreSQL-backed Temporal; a completed history witness
(`test:kind-baseline` on a fresh cluster); five Ready fixed/static apps in separate
namespaces; canonical diamond Services; both worker policies Ready; no existing
app ScaledObjects/discovery RBAC; isolated policy/scaling/discovery/RBAC suites
already passed/restored. Verify local kubeconfig/context, Docker kind ownership
and the known owned kube-system UID before selecting the safety variable.

```sh
export TASK10_TRUSTED_UID='<independently verified owned kube-system UID>'
mise run smoke
mise run test:resilience
```

These tasks build uniquely tagged images, graph/API-validate a combined overlay,
and temporarily change only apps and absent owned RBAC/scalers. Original Services,
Namespaces and ConfigMaps are not reapplied. Initial zero-replica bootstrap hands
off to native KEDA/HPA ownership. Both commands restore fixed/static baseline,
including app specs/ownership metadata and Service annotations, with UID guards.
They preserve current PVC, immutable Secret metadata, policies, Temporal Pod and
completed history. The Deployment controller's revision counter naturally advances.
Restoration/diagnostic failures propagate nonzero; keep the proof directory.

Each start writes an exact key-derived intent **before POST**. Cleanup is
registered before response assertions, pins validated run identities durably,
and reconciles ambiguous 503/lost responses directly with Temporal. NotFound
retains pending intent, unavailability is not absence, and no termination uses
an empty run ID. Only exact owned parent/history-derived child runs are cleaned;
there is no server-wide workflow cleanup. A successful response is not invented
for a failed HTTP assertion.

Printed proof folders `.local/task10-smoke-*` / `.local/task10-resilience-*`
are the scripts' stable naming, not a claim these runs occurred in Task10.
They retain commands' test/build/restore logs, original specs, created UID ledgers,
parent/child protobuf-JSON histories, ownership ledgers, identity comparisons and
exit. Failure diagnostics collect scoped events/app logs/scaler/registry/history,
not credentials or SQL logs. The top-level test budgets are 10 minutes for the
diamond and 30 minutes for sequential resilience; fixture build/deploy/rollback
have their own bounded operations. No deadline increase is needed for this task.

## Fourteen design acceptance criteria

Names below are actual tests or named shell assertions, not proposed coverage.
Current local result for each required command is in the execution section;
historical Linux coverage is separate. Unit suites run via `mise run validate`.

1. **Create cluster/infrastructure from documented commands.**
   `internal/chart: TestUpRerunAndBootstrapOrdering`,
   `TestInfrastructureAndBlockYAML`, `TestForeignKubeconfigFailsClosed` cover
   ordering/isolation offline. Real `k8s:up` from absence and seeded-history
   reapply passed locally. `test:kind-validation` proves ready native/custom
   dry-run controls, field-type/unknown-kind rejection and non-persistence.
   Historical Linux Kind also created/recreated the stack.
2. **Four annotated Services generate four valid registrations.**
   `tests/integration: TestDiscoveryPolicy` checks native generation/schema and
   lifecycle; `TestDiamondFromZero` waits for exactly the four canonical
   registrations/hash. `k8s/policies/tests: TestGeneration` checks CLI output.
   Run `test:policies-integration` then `smoke`; both passed locally and in
   historical bounded Linux Kind coverage.
3. **Zero workers while orchestrator remains available.**
   `TestInitialScalingConvergence`, `TestActivationFromZero`,
   `TestDiamondFromZero` assert zero desired replicas **and zero real Pods**,
   with healthy scalers and available API. Run `test:scaling` / `smoke`.
4. **Zero-body API, malformed body 400, durable key-only replay.**
   `services/orchestrator/api: TestStartRequestValidation`,
   `TestStartBodyStreamsAndContentType`, `TestHTTPDurableRepeatAndRegistryChange`,
   `TestHTTPConcurrentDuplicatesAndCompletedRetryAfterRestart`,
   `TestHTTPAmbiguousStartAndUnavailableLookup`, and
   `TestTemporalUnavailableRequestDeadline`; `runs:
   TestAmbiguousStartIsUnavailableUntilDurableRetry` cover transport/contracts.
   Named shell assertion `readiness, body-free diamond, ... completed keyed
   replay, body400` runs in Compose/kind baseline; `TestDiamondFromZero`
   verifies real replay after restart/registry change and no phantom body run.
5. **A, concurrent B/C, then D after both.**
   `services/orchestrator/workflows: TestParentScheduling` covers chains,
   diamonds and immediate-ready scheduling. `TestDiamondFromZero` inspects
   parent event IDs: both B/C commands precede either completion; D's command
   follows both completions. Log arrival order alone is not accepted evidence.
6. **Sorted identity-only result and command payloads.**
   `internal/contracts: TestSerializedWorkflowEnvelopesContainOnlyExecutionMetadata`,
   `services/worker/workflows: TestChildOptionsAndResult`, and
   `TestDiamondFromZero` compare full decoded parent/child/activity input/result
   values and stable queues from real histories. Extra fields cannot be silently
   discarded by permissive decoding. `TestHistoryAssertionsSensitivity` proves
   corrupt payload/queue/order/duplicate/missing-result witnesses are rejected.
7. **Eventual return to zero.**
   `TestActivationFromZero` and `TestDiamondFromZero` await zero replicas/real
   Pods after completion with healthy scaler conditions. `TestScalerErrorsAreNotInactivity`
   distinguishes unreachable endpoints from healthy empty queues. Poller-cache
   entries are not proof of living workers. `test:scaling` / `smoke` passed.
8. **Updates affect subsequent runs, not an active snapshot.**
   `TestPlanSnapshotIsolation` changes B from depending on A to C during an
   active run; full histories verify old/new input/hash/order. `internal/registry:
   TestSnapshotDeepCopy` and `runs: TestRetryKeepsOriginalValidPlanAndNewKeyUsesChangedPlan`
   cover immutable copies/key behavior offline. Run `test:resilience`: passed
   locally; historical PR Linux resilience was skipped, not passed.
9. **Invalid observed graph blocks starts before Temporal.**
   `TestLiveRegistry` introduces a cycle through separate updates plus unknown/duplicate/malformed
   state and checks diagnostic problems/503 plus direct no-execution lookup;
   `runs: TestNewStartsRevalidateCopiedPlans`, `TestNoStartOnLookupFailure`, and
   `internal/registry: TestKubernetesDecodeRejectsObservedInvalidState` provide
   focused pre-start coverage. Run `test:registry`: passed locally/historical CI.
10. **Removal/deletion leaves no usable stale registration.**
    `TestDiscoveryPolicy` separately checks source delete, flag false, annotation
    removal, re-enable/recreation and preexisting-source generation.
    `TestLiveRegistry` checks downstream removal and namespace scope lifecycle.
    Both live commands passed; readiness or synchronize=true alone is not proof.
11. **Admission rejects malformed/self dependencies on CREATE and UPDATE.**
    `k8s/policies/tests: TestAdmission` consumes the shared parser fixtures for
    CREATE/UPDATE/DELETE; `TestDiscoveryPolicy/admission` exercises real source
    Service requests, including padded self-reference and valid roots/multi-lists.
    Disable/removal and deletion remain possible. `validate` and
    `test:policies-integration` passed; admission is not retroactive repair.
12. **Complete intended and observed arbitrary graph validation.**
    `internal/plan: TestBuild`, `internal/manifest: TestInvalidFixtures`, and
    `cmd/validate-graph: TestCLI` cover unknown dependencies, self and longer
    cycles through the real CLI/full deployment set. `TestLiveRegistry` protects
    observed state outside that path. Run `test:manifests`,
    `validate:manifests`, `test:registry`. No CRD schema discovery is claimed offline.
13. **Separate namespaces, dynamic scope/RBAC, denied unauthorized access.**
    `internal/chart: TestStaticRender`, `TestDiscoveryRender` cover namespace and
    reader manifests. `TestLiveRegistry` exercises namespace add/remove and
    RoleBinding revocation/recovery without process restart; `TestRegistryRBAC`
    uses actual service-account impersonation across scope/verb allow/deny cases.
    Reader has Namespace and per-namespace ConfigMap reads only, no Secret/Service
    reads or writes. Both live commands passed locally/historical Linux CI.
14. **Persistence on replacement; destructive reset behaves as documented.**
    Named `compose:persistence` assertions cover Temporal/PG restart and container
    replacement, schema rerun, down/up, isolated reset old-history 404 and new
    execution. `test:kind-persistence` proves different Pod UIDs, unchanged PVC
    UID/binding and immutable Secret UID/version, schemas 1.19/1.14, exact
    completed run/hash/result and replay. Current local authorized down/down/up
    proved old witness 404; new seeded witness survived reapply and later tests.
    Historical Linux Kind also proved explicit reset 404. Reset loss is **not**
    preservation and raw histories are not an external database backup.

## Current Task11 local execution — 2026-11-11 UTC

Source/config: approved merged base `2ba4340facf6f0536bda64c7d0d35ffff7eda2e4`
plus Task11 **documentation-only** edits. No runtime, test, task, workflow, pin or
build changes. Documentation edits after these runs do not change executed
source/config. Darwin arm64 host; Docker 29.5.3 Linux arm64; Compose 5.1.4;
Go 1.27.3, kind 0.33.0, Helm 4.3.1, kubectl 1.35.8, Kyverno CLI 1.19.2.
Exact command/start/end/exit/whole-second elapsed files are ignored under
`.local/task11/<phase>.{command,start,end,exit,seconds,log}`. Durations are observed
on this cached-image/tool host, not clean-download performance guarantees.

All required public commands below exited **0**:

- `mise run validate`: **112s**, full race/vet, dedicated offline sensitivity,
  shell syntax, real pinned Kyverno CLI, Helm/full graph and Compose config.
  `mise run validate:manifests`: **1s**, explicitly run as its public entrypoint.
- `mise run images:build`: **3s**; `mise run compose:up`: **21s** including its
  cached rebuild, actual clean project/bootstrap and readiness. Unique project
  `temporal-demo-task11-docs-final-acceptance`, separate owner-only env file;
  all published host ports verified 127.0.0.1, UI GET succeeded.
- `mise run compose:smoke`: **22s**. `mise run compose:persistence`: **122s**,
  including schema versions, isolated reset 404 and fresh run. `compose:down`:
  **2s**, retained its volume; explicit task-project reset: **0s**, removed it.
  Ordinary retained temporal-demo Compose data/credentials were never touched.
- Authorized `mise run k8s:down`: **1s**; second down **0s** (absent).
  `mise run k8s:up`: **120s**, actual fresh cluster creation/readiness with cached
  tools/images. Pre-reset zero-running-execution, UID/Docker ownership and raw
  history/result capture gates passed. Old completed witness returned **404**.
- First fresh `mise run test:kind-baseline`: **19s**, seeds a completed witness
  **before** reapply. Second `mise run k8s:up`: **23s**; explicit API comparison
  of that seeded exact workflow/run/hash/result passed afterward.
  `mise run validate:k8s`: **1s**; `mise run test:kind-validation`: **2s**, all
  positive controls then intended negative rejections, no persisted fixtures.
  `mise run test:kind-persistence`: **31s** including safe schema rerun.
- `mise run policies:install`: **1s**; `mise run test:policies-integration`:
  **96s** (TestDiscoveryPolicy **93.13s**), with native lifecycle/preservation.
- `mise run apps:deploy-scaling`: **6s**;
  `SCALING_SKIP_LONG=0 mise run test:scaling`: **816s** including native rollback
  and baseline. Tests passed: initial convergence, error conditions, activation,
  replica handoff, **in-flight scale-down**, delayed retry and optional count.
  Singleton/diamond activation observations **5.328s/1.102s**; these are sampled
  scaling observations, not Pod-Ready startup promises. Long interruption reached
  attempt 2 after heartbeat timeout. Delayed retry observed activity backlog 1,
  workflow backlog 0 with zero real Pods; attempt 2 succeeded after reactivation.
- `mise run apps:deploy-discovery`: **13s**; `mise run test:registry`: **51s**;
  `mise run test:rbac`: **11s**; explicit native discovery restore: **5s**.
- `mise run smoke`: **171s** total, TestDiamondFromZero **149.00s**; proof
  `.local/task10-smoke-zlXSjfo0`. POST-to-desired-A-replica observation **4.370s**;
  all-worker-zero wait after completion/replay checks **118.873s**. That wait
  starts after other assertions and checks workers sequentially; it is not four
  independent cooldown measurements or first-task/Pod readiness latency.
- `mise run test:resilience`: **642s** total (Go package **595.565s**); proof
  `.local/task10-resilience-vW3jdZ87`. All five cases passed: snapshot **31.95s**,
  worker termination **187.24s**, orchestrator restart **96.44s**, delayed retry
  **220.45s**, orchestrator-only connectivity **58.91s**. Forced activity Pod UID
  selection and actual successful attempt 2 were observed. Connectivity cut is
  asserted <30s; **46.337s** is cut-start through healthy workflow completion,
  not outage duration. Bounded 503/direct NotFound before/after recovery passed.

Resilience fault injection routes only the orchestrator through a test-only TCP
relay. Workers/KEDA keep direct connectivity. It is **not a server-wide outage**.
Accelerated scale-down/delayed-retry fixtures use short cooldowns and longer retry
delays to expose interruption; production retry/grace/scaler settings stay unchanged.
Running-count visibility is approximate and cannot certify in-flight safety.

### Failures retained and resolved at their actual scope

No product behavior failure or code repair occurred. Two new ignored evidence
helper mistakes were preserved rather than relabeling the whole sequence green:

- `compose-sequence` exit **1**, after public build/up passed: the author's
  loopback assertion included **unpublished** exposed ports (`PublishedPort=0`,
  empty URL). Raw `compose-ports.json` established all actual published ports
  were loopback. Corrected the local assertion to filter published ports and
  resumed the existing project **before smoke**, without repeating up/build.
  `compose-resume` exit **0** includes smoke, persistence and owned cleanup.
- `pre-reset` exit **1**, read-only capture helper used child IDs containing `/`
  as flat filenames. Parent history had been captured; no reset occurred.
  Escaped slashes, rechecked zero-running/identity and captured available full
  parent/child histories/result; `pre-reset-capture-fixed` exit **0**. No
  production/test behavior change, test weakening or deadline increase.

### Reset and preservation provenance

Original trusted cluster UID `d3a21c85-e0a9-431e-ac0b-6fe597facfeb` and its PVC/
Secret/history were deliberately destroyed under the existing named-reset
permission, after safety gates. Pre-reset full available proof is under
`.local/task11/pre-reset/`, not a backup of PostgreSQL.

New verified cluster UID `5c66796f-1c23-4731-b4aa-eb06aee50b2a`; PVC UID
`dd199727-58a2-420c-9dd5-8bea72f39892`; immutable Secret UID
`6c37df44-c9d6-4740-8815-55351e976be0`, resourceVersion **508**. Only task-local
trusted UID was updated after confirming actual new node ownership. The fresh
seed witness is `run-0b24519c8266f6a0cda5f72a042baefd0e3c66a66239193ca7cde02e71f883e0`
/ `01a1ca7a-aa72-7d7e-8871-86b6f81b1f19`. Ordinary tests preserve the **new**
cluster; no claim is made that original history survived reset. Final health
checks passed: original fresh seed result still byte-identical, new PVC/Secret
identity unchanged, five Ready fixed/static apps, policies Ready and four
registrations, zero running executions and no owned scalers/HPA/discovery
RBAC/relay/Compose project or volume. Pending start intents in both combined
ledgers all have durable run-ID pins. Unrelated running container names, labels
and ports match preflight. Public `api:forward` and `ui:forward` each returned
localhost HTTP 200, then their exact owned process groups were terminated/reaped
and their ports verified closed. Raw proof: `final-health.*`,
`public-forwards.*`, `ledger-inspection.log`; final process inventory had no
remaining kubectl tunnel/integration-test processes.

## Cumulative selector/CI audit

`mise run validate` keeps all untagged packages under race detection/vet and
explicitly runs offline `HistoryAssertionsSensitivity`, `ActivityPodSelection`,
`WorkerRestoreKeepsNativeReplicas`, `SnapshotCleanupFailurePaths` and
`StartIntentOwnership` under `integration,resilience_integration`. It also checks
all scripts' Bash syntax, real policy CLI tests, chart/graph and Compose config.

Live selections remain distinct and uncached (`-count=1`):

- `policies_integration` + `^TestDiscoveryPolicy$` (20-minute process budget).
- `test:scaling`: `integration` without policy/registry/combined tags, default
  all selected tests, 25-minute budget. `SCALING_SKIP_LONG=1` skips **only**
  TestInFlightScaleDown in PR CI; local required acceptance used 0.
- `registry_integration`: exact TestLiveRegistry / TestRegistryRBAC selectors,
  20-minute budget each; discovery restore trap remains active.
- `acceptance_integration`: exact TestDiamondFromZero, 10-minute budget.
- `resilience_integration`: `^Test(PlanSnapshotIsolation|Recovery)`, 30-minute
  budget; selects all five live cases, not the relay-server fixture itself.
- Dedicated integration-tag offline sensitivity tests run in cumulative validate;
  ordinary `go test ./...` alone is not live acceptance. Selector/cleanup/shell
  guard tests remain active; no previous phase, test or image-build boundary was
  removed. No task/workflow repair was needed by the audit.

Historical exact-head Task10 Linux attempt 1 succeeded for Validate, Compose and
bounded Kind; raw logs/step JSON were inspected, including intentional long-case
skips. Full action pins/permissions/timeouts/artifact ownership and run links are
in [versions](versions.md). **Current Task11-head Linux CI, independent reviews
and manual hosted resilience remain pending controller gates**, not inferred
from earlier green PR jobs or current local ARM64 results.

## Known limits and failure handling

- Discovery is eventual; wait for intended names/hash. Registration is trusted
  derived state, not cryptographically authenticated provenance or worker capacity.
- Backlog/cooldown/optional running count cannot guarantee no scale-down interruption.
  Retry-safe activities, cooperative cancellation and bounded shutdown mitigate it.
  Do not remove workers or make incompatible upgrades during active runs.
- Pinned Kyverno ClusterPolicy deprecation remains visible. Helm's icon suggestion
  and secondary invalid-object diagnostics on intentional API negatives are not
  hidden. Earlier scaling log-stream strictness remains an environmental sensitivity;
  current full scaling passed without loosening assertions.
- Single-node local storage, no production auth/HA/external backup. Explicit
  reset loses history and deduplication. Clean startup timings above use local
  cached inputs, not a network-independent SLA.
- On failure keep logs/PIDs/ownership ledgers; run only native exact-owned cleanup.
  Failed restoration or unavailable ownership lookup is a failure, never evidence
  of absence. Do not broaden cleanup, silently skip a phase or blindly rerun.
