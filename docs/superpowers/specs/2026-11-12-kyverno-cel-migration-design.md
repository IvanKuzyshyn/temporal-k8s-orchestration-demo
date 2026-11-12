# Kyverno CEL policy migration design

## Intent and agreed constraints

Replace the demo's two deprecated Kyverno ClusterPolicy resources with CEL-based policies without changing worker admission or discovery behavior.

User decisions:
- The local demo cluster may be recreated; preserving existing workloads and Temporal history is not required.
- Keep Kyverno 1.19.2 unless an actual compatibility blocker requires reconsideration.
- The executor may delete and recreate the owned local demo cluster and run full startup and integration verification.

Deletion permission applies only to this checkout's owned `temporal-demo` cluster. Existing ownership/context checks remain mandatory. No cluster deletion happens during planning.

## Design

Keep filenames and metadata names stable:
- `k8s/policies/validate-workers.yaml`: `policies.kyverno.io/v1`, `ValidatingPolicy`, `temporal-validate-workers`.
- `k8s/policies/generate-registrations.yaml`: `policies.kyverno.io/v1`, `GeneratingPolicy`, `temporal-generate-registrations`.

Use one validation policy with two validations. Translate legacy deny logic into CEL predicates describing allowed input; guard the dependency validation internally rather than skipping the entire policy for nonworkers. Both policies retain namespace scoping to `temporal-demo.io/worker-namespace: "true"` and their ownership label `app.kubernetes.io/part-of: temporal-worker-discovery`.

### Admission contract

Only core/v1 Services on CREATE and UPDATE are validated. An absent `temporal-worker` annotation is allowed; a present value must be exactly `"true"` or `"false"`, including rejection of an empty value. For true workers, missing or whitespace-only dependencies are allowed. Otherwise each comma-separated token, after trimming exactly Go strings.TrimSpace whitespace, must match `^[a-z]([-a-z0-9]{0,61}[a-z0-9])?$` and differ from the Service name. Empty tokens are rejected; duplicate dependencies remain allowed. Preserve existing error messages and background evaluation. Invalid updates must be denied even when the old object was already invalid. No exceptions or relaxed enforcement are introduced.

### Generation contract

Select Services in the same namespace scope with annotation `temporal-worker: "true"`. Generate `v1/ConfigMap` named `temporal-worker-<service-name>` in the source namespace, labeled `temporal-demo.io/registration: "true"`.

Preserve all data exactly: `schemaVersion: "1"`, workerName, `<service-name>.v1.temporal` taskQueue, `workflowType: ExampleChildWorkflow`, raw runAfter (or empty string), sourceServiceName, sourceServiceNamespace, and sourceServiceUID.

Retain generation for existing matching Services, synchronization, and deletion of downstream resources when the generating policy is deleted. Test actual controller behavior for annotation opt-out, source deletion, and source recreation with a new UID. Do not assume CLI output proves reconciliation semantics.

### Lifecycle and rollout

Update installation collision checks and policy readiness waits in both `scripts/policies-install.sh` and `scripts/local.sh`. Keep foreign-resource refusal, strict server validation, bounded waits, and least-privilege RBAC. Verify supported readiness conditions against the pinned CRDs/controller.

Recreate the demo cluster rather than attempting an old/new generator ownership handoff. Repeated `up` on an initialized legacy cluster is not a migration mechanism: it skips bootstrap. Document the explicit destroy/recreate requirement. The integration runner continues refusing existing clusters and deleting only the cluster it created.

## Non-goals

No controller/chart/CLI upgrade, application-code changes, registration-schema changes, in-place migration or adoption procedure, new policy engine, or unrelated infrastructure refactoring. Upstream 1.19 charts may still ship legacy CRDs; those CRDs are not themselves legacy policy instances and need not be removed.

## Verification and acceptance

- Pinned CLI evaluates replacement policies and all existing parser fixtures without legacy API warnings.
- New CRDs are rendered and serve the chosen API version; actual server-side validation succeeds.
- Fresh startup and repeated startup wait on new policy types and reach a complete worker graph.
- CLI tests preserve admission outcomes and generated data, with expectations adapted to CEL's policy-level result semantics.
- Live tests cover admission, existing-source generation, synchronization, opt-out, deletion, UID replacement, and policy-deletion cleanup using isolated fixtures.
- Full `mise run validate` and `mise run test:integration` pass; no demo ClusterPolicy instances or ClusterPolicy deprecation warnings remain.

## Implementation choices delegated to executor

Determine exact CEL generation calls, evaluation fields, namespace-test fixture format, and readiness status from the pinned 1.19.2 CLI, chart CRDs, and version-matched docs/source. Verify Unicode trimming empirically against shared fixtures, using an explicit whitespace expression if necessary. Do not silently change contracts or upgrade pins if compatibility fails; report the concrete blocker first.
