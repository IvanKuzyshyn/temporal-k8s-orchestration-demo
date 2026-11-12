# Kyverno CEL Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace both demo ClusterPolicies with CEL policies while preserving admission, registration generation, and the full local lifecycle.

**Architecture:** Use one ValidatingPolicy and one GeneratingPolicy, preserving names, namespace scope, and the ConfigMap contract. Update both installation and resume readiness checks. Roll out on a recreated owned demo cluster; no in-place generator handoff.

**Tech Stack:** Kyverno CLI/controller 1.19.2, Helm chart 3.9.2, Kubernetes/kind, YAML/CEL, Bash, Go tests.

**Spec:** `docs/superpowers/specs/2026-11-12-kyverno-cel-migration-design.md`

## Global Constraints

- Keep Kyverno 1.19.2 unless an actual compatibility blocker requires reconsideration; do not upgrade pins without approval.
- Preserve ownership label `app.kubernetes.io/part-of: temporal-worker-discovery` and namespace selector `temporal-demo.io/worker-namespace: "true"`.
- Preserve all registration data and the exact dependency parser contract, including Go Unicode whitespace and duplicate acceptance.
- No application-code changes, in-place migration, or unrelated infrastructure refactoring.
- User authorizes deleting/recreating only this checkout's owned `temporal-demo` cluster, including its local Temporal history.
- Never bypass lifecycle ownership/context checks or weaken integration-runner preflight.
- Planning does not authorize executing this plan before review.

## Review Focus

- Missing maps/annotations versus explicitly empty flag: no CEL evaluation errors or accidental acceptance (Task 2).
- Unicode whitespace, duplicate tokens, and self-reference: match Go parser behavior exactly (Task 2).
- Existing invalid Service updated while still invalid: deny, with no grandfathering (Tasks 2 and 5).
- Worker opt-out/source recreation: no stale registration or stale UID (Task 5).
- Warm startup and foreign policy name collisions: use new resources without weakening ownership checks (Task 4).

---

## File map

- Modify `k8s/policies/validate-workers.yaml`: CEL admission predicates.
- Modify `k8s/policies/generate-registrations.yaml`: CEL ConfigMap generation and lifecycle settings.
- Modify `k8s/policies/tests/policy_test.go`, `generation_test.go`: pinned CLI contracts.
- Modify `internal/chart/infrastructure_test.go`: replacement CRD assertions.
- Modify `scripts/policies-install.sh`, `scripts/local.sh`: resource identity checks and readiness.
- Modify `internal/chart/local_resume_test.go`, `lifecycle_test.go` where their harnesses model policy waits.
- Create `internal/chart/policies_install_test.go`: installer collision/readiness regression tests following existing shell-stub conventions.
- Create `tests/integration/policies_test.go`: live, isolated policy fixtures and `demoCheckPolicyLifecycle(t *testing.T, root string)`.
- Modify `tests/integration/demo_lifecycle_test.go`: invoke that helper before normal graph/workflow assertions.
- Modify `docs/versions.md`, `docs/testing.md`, `docs/discovery.md`, `README.md`: current policy types, test ownership, and explicit reset instructions where relevant.
- Change `k8s/policies/rbac.yaml` or controller values only if pinned-version evidence proves necessary; retain least privilege and explain the change.

## Task 1: Verify pinned API and tooling compatibility

**Interfaces:** Consumes existing pins/render helper. Produces evidence for exact API fields, Ready conditions, and CLI test conventions used by Tasks 2–5.

- [ ] Run `mise exec -- kyverno version` and `mise exec -- go test -count=1 ./k8s/policies/tests ./internal/chart` to capture the pre-migration baseline. Distinguish existing failures from migration regressions.
- [ ] Render `mise exec -- bash scripts/render-controller.sh kyverno > /tmp/temporal-demo-kyverno.yaml`; inspect CRDs for `validatingpolicies.policies.kyverno.io` and `generatingpolicies.policies.kyverno.io`, served `v1`, evaluation fields, and status schema. Do not infer pinned capabilities solely from latest website docs.
- [ ] Check version-matched docs/source and CLI help for CEL generation syntax, namespace selector test data, policy-level results, and `--warnings-as-errors`. Use temporary minimal fixtures to confirm parsing/evaluation; do not replace the repository policies yet.
- [ ] Extend infrastructure assertions to require both replacement CRDs, including served `v1`. Keep valid upstream legacy-CRD coverage/count assertions; do not remove CRDs from the chart merely to remove warnings.
- [ ] Run `mise exec -- go test -count=1 ./internal/chart`; expect PASS. This capability test may already pass because the pinned chart ships these APIs.
- [ ] Record findings in this plan and commit the capability checks. If the pinned version cannot meet the spec, stop and report evidence instead of upgrading.

### Task 1 execution findings

Pinned chart 3.9.2 serves both CEL policy kinds at v1. Kyverno 1.19.2 CLI passed upstream validation and generation fixtures converted to v1 (3/3), with `--warnings-as-errors`. CEL test results use `isValidatingPolicy`/`isGeneratingPolicy`, no legacy rule name, and per-result `operation`. Generation uses `generator.apply(namespace, resources)`; generation evaluation switches are objects containing `enabled`. Readiness is nested at `.status.conditionStatus.ready` (not a top-level Ready condition). Namespace selector fixture labels remain supported. Baseline and full race-enabled Go suite passed.

## Task 2: Migrate worker admission with behavior parity

**Interfaces:** Produces `ValidatingPolicy/temporal-validate-workers` at the existing file path. Consumes Task 1's test format; keeps existing error messages and parser fixtures.

- [ ] Adapt `TestAdmission` to CEL policy-level results while preserving CREATE/UPDATE/DELETE, namespace, and non-Service coverage. Add a manifest identity assertion requiring `policies.kyverno.io/v1`/`ValidatingPolicy` and Deny enforcement.
- [ ] Add explicit cases for absent annotations map, empty annotations map, absent flag, empty flag, `false` plus invalid dependencies, Unicode whitespace, duplicates, self-reference, and an UPDATE with invalid old/new values. Keep all `internal/plan/testdata/dependencies.json` cases; assert outcomes, not legacy rule names or counts. A guarded dependency expression may return pass rather than a legacy rule's skip.
- [ ] Run `mise exec -- go test -count=1 -run '^TestAdmission$' -v ./k8s/policies/tests`; expect failure against the legacy manifest/new identity requirement.
- [ ] Replace the policy with one CEL ValidatingPolicy: core/v1 Service CREATE/UPDATE matching, unchanged namespace selector, Deny, background evaluation, and two validations. Guard dependency checks inside their expression so the flag validation is never accidentally skipped. Preserve missing-versus-empty semantics and the exact Service-name regex.
- [ ] Verify CEL trimming against every fixture and the explicit Go whitespace set in the old manifest; implement explicit trimming if the built-in differs. Do not normalize stored runAfter or change parser fixtures to accommodate CEL.
- [ ] Run the focused command again; expect PASS with no CEL evaluation errors. Add `--warnings-as-errors` to CLI invocations if confirmed supported in Task 1.
- [ ] Commit admission manifest and tests together.

## Task 3: Migrate registration generation

**Interfaces:** Produces `GeneratingPolicy/temporal-generate-registrations` at the existing path and the unchanged ConfigMap data contract consumed by discovery.

- [ ] Adapt `TestGeneration` to the pinned CEL CLI result/output format. Assert the new kind/API, same generated identity, all eight data fields, and namespace exclusion. Add a matching-namespace false/missing flag case and a missing-dependencies case expecting empty runAfter.
- [ ] Run `mise exec -- go test -count=1 -run '^TestGeneration$' -v ./k8s/policies/tests`; expect failure against the old manifest.
- [ ] Write CEL generation expressions and verified `spec.evaluation` settings preserving generateExisting, synchronize, and `orphanDownstreamOnPolicyDelete: false`. Preserve raw dependency text and use the triggering Service's UID/namespace/name, not fixed values. Guard optional annotation access.
- [ ] Run the focused command and then `mise exec -- go test -count=1 -v ./k8s/policies/tests`; expect PASS and no legacy warnings. If CLI cannot expose generated output for this type, preserve the positive/negative output assertions in a pinned-engine or live test rather than deleting coverage.
- [ ] Commit generation manifest and tests together.

## Task 4: Wire installation and warm startup to new policy identities

**Interfaces:** Consumes Tasks 2–3 resources; produces safe installation and Ready waits for both fresh and initialized clusters.

- [ ] Add `TestPoliciesInstallCELResources` and `TestPoliciesInstallRefusesForeignResources` in `internal/chart/policies_install_test.go`, using shell stubs consistent with nearby lifecycle tests. Assert both fully qualified new policy identities, strict server dry-run before apply, readiness checks, and rejection of each foreign-owned policy/role before any mutation.
- [ ] Extend warm-start coverage to assert new policy waits and reject legacy policy waits in `scripts/local.sh`. Adjust bootstrap harness expectations only where affected.
- [ ] Run `mise exec -- go test -count=1 ./internal/chart`; expect failures for legacy resource references.
- [ ] Update collision checks and readiness targets in `scripts/policies-install.sh` and `scripts/local.sh` to the new fully qualified types. Use the actual readiness conditions verified for the pinned controller; preserve 90-second install and 120-second resume bounds. Retain RBAC aggregation and context checks.
- [ ] Run `mise exec -- go test -count=1 ./internal/chart` and `mise exec -- bash -n scripts/policies-install.sh scripts/local.sh`; expect PASS.
- [ ] Search repository references to `clusterpolicy` case-insensitively. Classify remaining upstream CRD assertions/historical docs separately; no active install/readiness path may target legacy policies.
- [ ] Commit lifecycle wiring and regression tests.

## Task 5: Prove live admission and generation lifecycle

**Interfaces:** Adds `demoCheckPolicyLifecycle(t *testing.T, root string)` in a `demo_integration`-tagged file; call it from `TestDemoLifecycle` before graph assertions. The helper leaves demo policy/resources unchanged when it returns.

- [ ] Add isolated test namespaces: one labeled selected and one unselected. Use no worker Deployments and clean up Services/ConfigMaps/namespaces before returning, so fixture registrations cannot pollute the demo graph.
- [ ] Add live denial assertions for malformed flags/dependencies on CREATE and UPDATE and acceptance for equivalent unselected resources. For invalid-existing UPDATE, seed an invalid Service in the unselected namespace, select that namespace, and require a subsequent still-invalid update to fail. Assert policy-specific denial, not merely any kubectl error.
- [ ] Add bounded eventual assertions for generation, raw runAfter updates, repair after ConfigMap tampering/deletion, flag removal/false cleanup, Service deletion, and recreation with the same name but a different UID.
- [ ] Test generateExisting and policy-deletion cleanup with a temporary, uniquely named copy of the new generation policy scoped to a separate fixture namespace. That namespace must lack the demo selection label, so the production policy cannot also generate there. Create the matching Service first, install the copied policy, await generation, then delete the copied policy and await downstream deletion. Never delete/recreate the real demo policy for this assertion.
- [ ] Wire the helper into the existing lifecycle test, which the runner already selects by exact name. Preserve existing workflow, history, RBAC, zero-scaling, and resume coverage. Use bounded polling and cleanup on failure, not fixed sleeps.
- [ ] Add assertions that the two expected new policies are ready and the two old ClusterPolicy instances are absent. Capture evidence that fresh startup logs do not contain the ClusterPolicy deprecation warning.
- [ ] Run the live verification sequence below. The new tests must demonstrably reject an incorrect field/predicate or disabled synchronization in a controlled test run; revert that intentional mutation before final verification. If a promised lifecycle behavior differs on 1.19.2, investigate rather than weakening assertions.
- [ ] Commit live regression coverage after restoring correct manifests and obtaining passing results.

## Task 6: Documentation and final acceptance

**Interfaces:** Consumes all migrated behavior and verification evidence; produces an actionable reset procedure and accurate test documentation.

- [ ] Update `docs/versions.md` to remove acceptance of deprecated-policy warnings and record the retained version pins. Update discovery/testing docs to explain CEL policies and lifecycle coverage.
- [ ] Document in `README.md` that initialized legacy clusters must be explicitly destroyed/recreated; repeat `up` skips bootstrap and does not migrate installed policy kinds. Warn that destroy deletes local Temporal history. Do not teach users to bypass foreign ownership checks.
- [ ] Run `mise run validate`; expect exit 0 for formatting, uncached race tests, vet, shell syntax, CLI policy checks, and manifest rendering.
- [ ] Run the complete live sequence below against the final tree and record commands/results. No success claim based solely on dry-run or CLI tests.
- [ ] Run `git diff --check`; review all changes for relaxed scope, broad RBAC, accidental pins changes, or missing parser cases. Commit documentation and report final verification evidence and any limitations.

## Authorized live verification sequence

1. Confirm Docker is available and no lifecycle command is active. Inspect checkout-local ownership records and current node identity. For execution in a worktree, do not copy ownership files to claim another checkout's cluster; run authorized cleanup from its owning checkout, or stop if ownership cannot be established.
2. Run `mise run destroy` from the owning checkout. User has authorized losing this local demo's data. If ownership checks refuse, stop; do not use raw Docker deletion as a workaround.
3. Run `mise run test:integration` from the implementation checkout. It requires no existing demo cluster/state, invokes public `up`, runs the tagged lifecycle test (including Task 5), and destroys its own cluster. Expect exit 0. Do not run standalone `up` first: that would violate the runner's preflight.
4. Run `mise run up` to leave a usable migrated demo; inspect `.local/lifecycle/up.log`, policy statuses, generated registrations, and `/graph`. Save warning evidence before repeated up overwrites the startup log. Expect a ready complete graph and no ClusterPolicy deprecation warning.
5. Run `mise run up` again; expect successful readiness against the new policy types with no reinstall or resource churn. Leave this cluster running unless the user requests otherwise.

## Rollback

Revert the migration commits and use the guarded destroy/recreate flow on the owned demo cluster. Do not apply legacy and new generators concurrently. Rollback also discards local demo history and requires full verification; it is not an in-place ownership handoff.

## Execution evidence (2026-11-12)

All six tasks implemented in a GitHub stack, one PR per task. Kyverno/chart/CLI pins, RBAC, and application code remain unchanged.

- Tasks 1–4: pinned API probes and race-enabled Go suite passed; admission and generation identity tests failed before conversion and passed afterward; installer/resume regressions failed before changing legacy references and passed afterward.
- Task 5: full disposable integration passed repeatedly. A deliberate `synchronize.enabled: false` mutation failed `CELPolicyLifecycle` with a registration reconciliation timeout; restored policy passed. Legacy-instance absence checks separate stdout from stderr because querying the deprecated endpoint itself emits a warning even when the resource is absent.
- Task 6: `mise run validate` passed; final full integration passed in 426.97s (CEL policy subtest 9.14s). Fresh `mise run up` and repeated `mise run up` passed. Both policies reported `.status.conditionStatus.ready=true`; all four registrations retained their UIDs and data across repeated startup; `/graph` was synchronized and valid. Fresh startup log had no ClusterPolicy deprecation warning.
- Environment: macOS arm64, Docker aarch64. No pre-existing demo cluster was present; all verification clusters were created by this worktree with ownership checks intact.
- Review: author self-review only; no independent reviewer tool is available. PRs remain unmerged for human review.

## Planning review

Self-review: all specified behaviors map to Tasks 2–5; both installation and warm startup are covered; integration preflight ordering is explicit; version compatibility is a gate rather than an assumed field mapping. No subagent review tool is available in this session, so this is a self-reviewed plan. No implementation or runtime verification has been performed by preparing this document.
