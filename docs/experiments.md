# Combined acceptance and recovery experiments

## Commands and prerequisites

Use the pinned tools with `mise`. This is application-only acceptance on an
already deployed, owned kind stack, **not** a provisioning or reset command.

Prerequisites: PostgreSQL-backed Temporal with at least one completed history
witness; five Ready fixed-replica application Deployments in their separate
namespaces; the two installed Ready worker policies; canonical diamond Service
annotations; no unrelated running Temporal executions; no preexisting discovery
RBAC or ScaledObjects. Existing static scaling, policy, discovery, RBAC and
persistence suites remain independent and unchanged.

Explicitly verify the worktree-local kubeconfig, `kind-temporal-demo` context,
`temporal-demo` Docker node ownership, and the expected kube-system UID before
setting `TASK10_TRUSTED_UID`. Do not derive trust blindly from whichever cluster
is currently selected. On a newly provisioned **disposable CI runner**, the
workflow records the UID of the cluster that job just created. Never use this
mechanism to adopt or reset a foreign cluster.

```sh
export TASK10_TRUSTED_UID='<independently verified owned kube-system UID>'
mise run smoke
mise run test:resilience
mise run validate
```

The public tasks render the combined discovery/scaling overlay, validate the
complete graph through `plan.Build` and strict API validation, build uniquely
tagged application images, and install only app Deployments and absent test-owned
RBAC/Scalers. They do not reapply original Services, Namespaces or ConfigMaps.
KEDA retains native HPA ownership after the initial zero-replica bootstrap.

`scripts/smoke.sh` supplies bounded localhost API/Temporal tunnels and mandatory
`DEMO_API_URL`, `TEMPORAL_ADDRESS`, `DEMO_KUBE_CONTEXT=kind-temporal-demo` and
`SCALING_ROOT`. Tests fail if these are absent, rather than silently skipping.
API tunnels are reopened after Pod replacement. The diamond has a ten-minute
whole-test deadline; deployment/build and bounded safety rollback are separate
fixture work. The sequential long suite has a thirty-minute process deadline;
production parent/child/activity/retry/scaler timeouts are not increased.

Proof directories are printed as `.local/task10-smoke-*` and
`.local/task10-resilience-*`. Keep them, especially on failure. They contain
actual execution IDs, full protobuf-JSON parent/child histories, exact original
app snapshots, created-resource UID ledgers, pre/post preservation comparisons,
raw build/test/restore logs and final exit status. On failure,
`scripts/collect-diagnostics.sh <proof-directory>` also captures scoped Pod events,
app logs, ScaledObject conditions, registrations and available histories. It
never reads Secrets or SQL logs. Credential **identity** capture is limited to
UID/resourceVersion/immutable, never credential values. Diagnostics failures and
rollback failures propagate as nonzero exits.

## What is asserted

- Exactly the four expected generated registrations in the four worker
  namespaces, with the canonical full plan and hash. All four workers have zero
  desired replicas **and zero real Pods**, with healthy Ready scalers.
- A body-free POST starts the diamond. Temporal parent event IDs prove A
  completion precedes B/C commands and both B/C completions precede D. B/C must
  both be scheduled before either completes, not serialized by a global barrier.
  Each child is initiated/started/completed once on its expected stable queue.
- Parent input is precisely the immutable plan; child/activity commands and
  results contain only worker identity. Complete JSON values are compared so
  additional business-data fields cannot be ignored by permissive struct decode.
  Child activity commands and successful results are inspected in child history.
- Sorted identity-only parent results are returned by the real API. The same key
  returns the original workflow/run/hash/status/result after completion,
  orchestrator restart and a registry dependency change. Unexpected whitespace
  body bytes produce 400 and direct Temporal lookup establishes no phantom run.
- Workers return to zero real Pods after completion/replay. Cached Temporal
  poller entries are observed, but are never treated as proof of running workers.
- Snapshot isolation changes B's dependency from A to C while the first parent
  is still active; the old execution retains its original input/hash and diamond
  execution order, while a different key selects and executes the changed plan.
- Recovery force-deletes the exact Pod identified by the running A activity's
  worker identity/UID, observes successful attempt >=2 in history, restarts the
  orchestrator during execution, and reuses the independent delayed-activity
  retry proof against the combined deployment. The latter observes activity
  backlog with zero real worker Pods, then native reactivation and retry >=2.
- Connectivity recovery routes **only the orchestrator** through a test-only TCP
  relay. Removing only the relay Pod closes established connections; a previously
  healthy gRPC path then demonstrably fails. The API returns bounded 503, and the
  direct healthy Temporal client proves the rejected key is NotFound both during
  the outage and after recovery. The existing run and durable replay recover
  without restarting that API process. This is **not** a total-server or
  all-client outage experiment. Workers/KEDA keep their original direct endpoint.

The relay uses the integration test binary itself and existing pinned Go/Debian
base images, not a production binary or test switch. Its outage rollback is
registered before disconnecting. The deliberate cut interval is asserted below
thirty seconds; relay startup/reconnection and remaining workflow completion
are separate from that cut interval.

## Retained local observations — 2026-11-10

Provenance: Darwin arm64 host, Docker Desktop's Linux arm64 daemon, existing
retained kind cluster. These are local retained-cluster measurements, **not**
fresh-cluster or hosted Linux results. App image tags identify base
`233c8697f5eef844ae17a8bddf914e2af4d64e14` plus unique dirty-worktree suffixes;
they do not claim clean exact-head CI verification.

Observed clients: Go 1.27.3, kind 0.33.0, Helm 4.3.1, kubectl 1.35.8,
Kyverno CLI 1.19.2 (`e63308f1`), Docker client/server 29.5.3. Repository pins
remain Kubernetes 1.35.8, KEDA chart 2.21.0, Kyverno chart 3.9.2,
Temporal server/admin-tools 1.32.0, Temporal SDK 1.50.0/API 1.63.5 and
PostgreSQL 16.15. `versions.log` and existing manifests retain exact provenance
and image/chart digests; no version pin was changed for these experiments.

Successful `mise run smoke` invocations:

- `.local/task10/smoke-3.log`, exit 0; proof
  `.local/task10-smoke-piyzXOFL`. Diamond test 146.18 s; observed POST-to-desired-A
  replica=1 latency 4.295 s; combined wait for all workers to be zero after the
  completion/replay checks 111.716 s. This is a desired-replica observation, not
  a measured Pod-Ready or first-task-start latency.
- `.local/task10/smoke-4.log`, exit 0; proof
  `.local/task10-smoke-yriTbAtI`. Diamond test 156.81 s; same activation observation
  4.302 s; all-worker zero wait 117.163 s. Initial zero-real-Pod checks coexisted
  with cached Temporal workflow/activity poller counts A=3/3, B=2/2, C=2/2,
  D=2/2: poller-cache entries are demonstrably not living Pods.

The zero waits start after API restart/replay/dependency checks, not exactly at
activity completion; per-worker probes run sequentially. These values must not
be interpreted as four independent cooldown measurements. Native configured
polling/cooldown remain 5 s/120 s.

Successful `mise run test:resilience`: `.local/task10/resilience-3.log`, exit 0;
proof `.local/task10-resilience-EJqkifCl`; Go package elapsed 441.947 s:

- Snapshot isolation: 26.32 s, both old/new plans validated through real histories.
- Worker termination: 164.97 s including initial zero wait and restoration;
  forced exact Pod UID `62d43fce-e7e1-46c3-89c6-d4a56c38fef6`; completed activity
  attempt 2.
- Orchestrator restart: 47.86 s including restoration; durable identity/result and
  real history checks passed.
- Delayed retry: 137.39 s; heartbeat failure, delayed attempt 2, observed activity
  backlog 1/workflow backlog 0 with zero real Pods. Desired replica=1 sampled
  39 ms after unpause completed; that is not an end-to-end cold-start measurement.
  Final observed worker zero wait 15.246 s under the experiment's short cooldown.
- Scoped connectivity: 64.73 s including setup/restoration; actual failed gRPC,
  503 within the asserted transport bound, NotFound before/after recovery, and
  completed original run/replay. The raw log's `observed outage=52.845s` label was
  misleading: its timer ran from cut start through **healthy workflow completion**,
  not just the disconnected interval. Code now labels that quantity accurately;
  no precise disconnected-duration estimate is claimed from that run.

Baseline and changed canonical hashes were respectively
`5fefcdcc52af25c9c21a523f1f3c1f7d928018051ffe89ac8b5a0e6ccf8c3b62` and
`88ce5ea75d2a5a0a600ea3f636a6a9c70171b2fe9a2932aade940e9c42bf6bc8`.

## Failures retained, not erased

- Test-first diamond failed on static registry mode before combined deployment
  (`diamond-red.log`, exit 1). This was the expected missing-wiring RED.
- First public smoke failed **before mutation**: kubectl emits a stream of JSON
  objects, not a List; Bash 3 also rejects an empty array under nounset.
  Native-pipeline and empty-cleanup regressions reproduced both, then passed.
- Second smoke's diamond passed in 152.84 s, but the public command failed
  restoration. Removing the newly created ServiceAccount too early prevented the
  baseline template from creating a replacement Pod. Exact UID-scoped temporary
  ServiceAccount repair restored Ready baseline; subsequent cleanup retains
  access until restored Pods are Ready. The comparison also incorrectly treated
  the Deployment controller's revision counter as restorable configuration.
  The counter is now the only annotation excluded; exact app specs/UIDs/labels,
  rollout annotations and last-applied ownership bytes are still compared.
- First resilience invocation failed before mutation because the root Docker
  allowlist excluded test sources. A fixture-specific Docker ignore file fixes
  that without expanding production build contexts.
- Second resilience ran all cases but failed worker termination's single-Pod
  assumption during rolling replacement, and connectivity returned no bounded
  HTTP response. Selection now uses the running activity's actual worker
  identity/Pod UID; tests wait for old real Pods to be gone before configuring
  the next activity. The API defect was genuine: start/status SDK calls had no
  server deadline (only readiness did). New deadline regressions failed first;
  start/status now have a two-second operation budget and return 503 on Temporal
  unavailability. No workflow, retry, shutdown, scaler or readiness timeout was
  increased. Subsequent full resilience passed.

Raw commands/exits and focused RED/GREEN/sensitivity evidence live in
`.local/task10/`; detailed case histories and cleanup logs remain in each printed
proof directory. Synthetic corruptions reject extra payload fields, wrong queues,
invalid dependency ordering, duplicate children and missing results. No fake
failure knob or private production-state injection was used.

## Preservation and outstanding verification

Ordinary runs preserve their own pre-test baseline; that guarantee does not
extend across an explicitly authorized destructive cluster reset. The original
cluster's preservation observations below are **historical pre-reset proof**.

Successful runs restore the original five application specs and last-applied
metadata, exact source Service identity/spec/annotations, bound PVC,
immutable Secret UID/resourceVersion, policy identities/specs, Temporal server
Pod identity, and completed history hash. Only recorded owned executions can be
terminated. Created RBAC/scalers/relay objects are deleted only after matching
their recorded UIDs. The controller-owned Deployment revision counter naturally
advances; it is not reset. Failure evidence and snapshots are retained.

The original pre-experiment witness
`run-5669836d56f2695ce29d0eb4c67ff471e236e700ce2d9d3a7e9acb96bf394d64` /
`01a1c348-8058-7a5a-808f-b5f4e8c5db57` retained SHA256
`2bef29cb6361ea06ab4c84a5e5479fa793211c96b85f0d9e4b666adcb39db87d`
after the recovery suite (`original-witness-after.log`, exit 0).

Fresh-local recreation was initially blocked by the no-reset safety boundary.
The user subsequently explicitly authorized deleting `temporal-demo` for testing,
as recorded in `task-10-recreation-authorization.md` in the task workspace.
That authorization superseded only the cluster-preservation restriction for one
deliberate local reset. No separate VM was required or provisioned. The fresh-local
acceptance below resolves that verification gap; earlier retained-cluster runs
are not being relabeled as fresh evidence.

Automatic PR CI retains all earlier phases and adds the same public smoke before
and after its existing **disposable hosted runner** recreation. Manual dispatch
also runs the same public resilience task. Secret-free combined proof directories
are uploaded even on failure; kubeconfig/credential files are not included.
Actual exact-head hosted CI has not been run by this implementer and cannot be
claimed as evidence. Fresh-local results below are distinct from the earlier
retained-local results and from prospective hosted Linux proof. Independent task
and whole-branch reviews, controller verification and actual exact-head cumulative
Linux CI remain pending. No Task10-complete, Task11 or PR-ready claim is made.

Final offline `mise run validate` passed (exit 0; `.local/task10/validate-final.log`
and `.exit`), including full race tests/vet, dedicated acceptance sensitivity,
Bash syntax, pinned policy tests, chart/graph checks and credential-free Compose
configuration. Final read-only original-snapshot/history and absence-of-owned-
resources checks also passed (`.local/task10/preservation-final.log`, exit 0).
Those checks preceded the later authorized reset; validation/resilience were not
rerun merely because the environment was recreated.

## User-authorized fresh-local acceptance — 2026-11-10

Only the previously missing fresh-local acceptance was executed, on the same
Darwin arm64 host and Docker Desktop Linux arm64 daemon. No kind identity-boundary
change, VM installation, Docker-context switch, broad Docker cleanup or Compose
reset occurred. No application/test/config code changes were needed.

Before deletion, the worktree-local mode-600 kubeconfig/context, original
kube-system UID `91d1ae13-a854-471e-a044-040a669f28ef` and exact control-plane
Docker kind label `temporal-demo` were reverified. There were no active experiment
processes/tunnels, native scalers/HPAs or relay, and no running Temporal executions.
The original completed witness still had its recorded hash. Its full available
parent/child histories, app/Service/PVC/policy identities and credential identity
only (not values) were captured to `.local/task10/recreation/pre-reset/`.
Read-only pre-reset checks and history capture exited 0.

Actual commands, all through the existing pinned tasks, all exit 0:

- `mise run k8s:down`: 2 s, deleted only the explicitly authorized named cluster.
- `mise run k8s:up`: 182 s, genuinely created a new cluster/PVC and provisioned the
  pinned stack. Initial app images used clean committed source
  `d926577695e404ff750d25b8541e49f31c04532f` (no implementation changes).
- `mise run policies:install`: 2 s, required policies on the fresh stack.
- `mise run test:kind-baseline`: 21 s, created the new completed-history witness
  required by smoke's safety preflight, using the same fresh-run prerequisite as
  CI. This is fresh fixture initialization, not a repeat of retained resilience
  or full validation.
- `mise run smoke`: 171 s including fixture deployment and successful rollback;
  actual diamond test 142.16 s (Go package 142.697 s).

Command elapsed values are whole-second shell observations, not subsecond
benchmarks. Raw logs, exits and elapsed files are under
`.local/task10/recreation/{down,up,policies,seed-baseline,smoke-fresh}.*`.

New identities:

- kube-system UID `d3a21c85-e0a9-431e-ac0b-6fe597facfeb`;
- bound PostgreSQL PVC UID `4c8de2e2-9a4f-4cd6-a474-41a2e87489b6`, volume
  `pvc-4c8de2e2-9a4f-4cd6-a474-41a2e87489b6`;
- new immutable Secret UID `150b327e-3ce2-456d-8b1e-ad3583ca8615`, resourceVersion
  `460`. Its identity was unchanged across the ordinary fresh smoke.

The old witness returned HTTP **404** on the new healthy API before seeding new
history (`fresh-boundary.log`, exit 0; `old-history-http-status` and
`old-history-response.json`). **Loss of the old cluster's histories/PVC was
expected and explicitly authorized.** Do not claim their old identities or
history survived this reset. Old raw proof remains on the host, not in Temporal.
The new verified UID was selected only after confirming the newly created owned
cluster; the worktree-local task environment was updated, not global kubeconfig.

Fresh combined smoke proof: `.local/task10-smoke-Gkmtgsul`, with actual histories,
created UID ledger, exact fresh-baseline before/after comparisons and restore log.
It passed exact registration/hash checks, zero actual Pods, full diamond command/
payload/queue/order/result assertions, body400/no phantom run, durable replay
across completion/restart/registry change and eventual zero. Desired-A-replica
activation was sampled at 1.104 s; combined all-worker zero wait after replay
checks was 111.602 s. These have the same limited measurement definitions as
above, not Pod-Ready latency or independent worker cooldowns. Cached workflow and
activity poller counts were 1/1 for each worker while actual Pods were zero.

The fresh seed witness
`run-405e7ae4861bd2fe761d1b1e18638c771fb3e4d40ef2c85c27b19114f949e157` /
`01a1c78d-b7a9-7f0a-862d-fcfd46fa6c17` retained hash
`f22fa961095286cfac3f3787b6155c7c6dfef015b882dbc7e9e5199c8b4c0fd0`
across this ordinary smoke. That is a **new-cluster** preservation claim only.

Final read-only health/process checks exited 0 (`final-health.*`,
`final-process-check.*`): recreated Temporal/PostgreSQL and all five fixed
application Deployments healthy, both policies Ready, new PVC/Secret preserved
across smoke, no test-owned scaler/HPA/discovery RBAC/relay or tunnels/processes
remaining. Smoke cleanup also established no running executions and unchanged
fresh witness hash. The cluster is left running in its restored fixed-replica,
static-registry baseline, with four generated registrations and policies ready
for future explicit combined tests. The fresh Pod inventory records one early
KEDA operator restart; it is currently Ready, and neither provisioning nor smoke
failed. No further teardown was performed.

## Review fix round 1 — 2026-11-11

Independent review found failure paths not covered by the earlier happy-path
runs. Per-experiment worker Deployment rollback now restores the Pod template
and intended metadata with a UID guard, without replacing `/spec` or writing
KEDA/HPA-owned replicas. Final fixed-replica shell baseline restoration remains
after scaler removal. Snapshot-isolation rollback uses independent cleanup
callbacks: restoring the scaler cannot be skipped by a fatal Service/registry
assertion, and an expired experiment context is not reused for registry cleanup.
Both callback failures remain visible. ClusterRole/ClusterRoleBinding cleanup
now uses cluster-scoped arguments; namespaced resources keep their namespace,
and diagnostic stderr is not suppressed. Pinned policy deprecation is unchanged.

Focused regressions each failed first, then passed: application of the actual
worker cleanup JSON patch to a Deployment whose native replicas advanced from
0 to 1; real `testing.T` cleanup/FailNow subprocesses with Service, scaler,
both-action and registry verification failures plus an expired experiment
context; and shell cleanup scope/diagnostic propagation. These offline checks
are included in validation's dedicated build-tag selection; raw RED/GREEN and
focused race-test output is under `.local/task10/fix-1/`.

Affected public commands were each run once against the unchanged recreated
cluster, without another reset: smoke exit 0 (216 s including fixture work,
diamond 145.89 s) and resilience exit 0 (627 s including fixture work,
five-case Go package 582.709 s). The additional native zero waits in recovery
remain within existing budgets; no timeout or assertion was relaxed. Proof
folders are `.local/task10-smoke-rFcrgPfS` and
`.local/task10-resilience-rcpA2y3S`. All original app/Service ownership metadata,
new-cluster PVC/Secret/policy identities and completed witness were preserved
across these ordinary tests. Final checks found Ready fixed apps, no test-owned
scaler/HPA/discovery RBAC/relay or worktree-owned tunnels, and zero running
executions. Scope warnings were absent in both restore logs; policy warnings
remain visible.

No repeated full validation was run for this fix round. The controller's
separate external 240-second validation interruption has no established final
exit and is neither a product failure nor a passing run. Prior author validation
is historical evidence, not validation of the new failure paths. Controller
re-review and exact-head cumulative Linux CI remain separate pending gates.
