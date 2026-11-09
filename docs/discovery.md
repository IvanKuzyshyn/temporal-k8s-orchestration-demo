# Worker registration policies

Task 8 installs admission and registration infrastructure only. The orchestrator
continues using `REGISTRY_MODE=static`; Kubernetes registry wiring and its
read-only service account permissions belong to Task 9.

## Run

```sh
mise install kyverno
mise run policies:test                 # real pinned CLI, no cluster
mise run policies:install              # already-ready, owned kind-temporal-demo
mise run test:policies-integration     # temporary namespaces; restores policies
```

Use the worktree's `.local/kubeconfig`, never a global context. Installation
refuses foreign resources occupying the three owned policy/RBAC names. The live
test temporarily removes **only** these two owned ClusterPolicies to prove
installation after a source exists, then restores their checked-in definitions,
even after assertion failures. Use the dedicated demo exclusively during this
test. Do not edit its policies concurrently. A completed baseline workflow must
already exist (`mise run test:kind-baseline` on a new stack); the test hashes its
complete history before/after and preserves app Service UIDs, PVC identity,
immutable Secret metadata, and static registry mode. It never resets storage or
restarts app workloads. Automatic kind CI runs baseline/persistence first, then
these policies and lifecycle checks, retaining scaling and reset coverage.

## Admission contract

Only Services in namespaces labeled
`temporal-demo.io/worker-namespace: "true"` are selected. On CREATE/UPDATE,
`temporal-worker`, when present, must be the string `"true"` or `"false"`.
Missing/false allows deregistration regardless of stale dependency text. DELETE
is never validated, including deletion of preexisting invalid Services.

For true registrations, `temporal-worker-run-after` is an optional comma-separated
list. Missing or wholly blank means a root. Individual names are trimmed with
Go's Unicode White_Space set, then checked against the RFC 1035 Service-name
syntax and length, including rejection of empty entries and trimmed self-reference.
Duplicates are allowed; the shared Go parser deduplicates/sorts them. Unknown
dependencies and cycles are deliberately admitted: only `plan.Build` has the
complete graph. Parser fixtures live in `internal/plan/testdata/dependencies.json`
and are consumed by both Go parser tests and actual CLI/API tests.

## Generated data

Each selected true Service generates a same-namespace ConfigMap named
`temporal-worker-<service-name>` with label
`temporal-demo.io/registration: "true"`. All data values are strings:

```yaml
data:
  schemaVersion: "1"
  workerName: worker-a
  taskQueue: worker-a.v1.temporal
  workflowType: ExampleChildWorkflow
  runAfter: ""
  sourceServiceName: worker-a
  sourceServiceNamespace: temporal-demo-worker-a
  sourceServiceUID: <actual Service UID>
```

The dependency annotation is preserved verbatim; normalization remains the Go
parser's job. No business fields are emitted. Namespace separation avoids
ConfigMap collisions, not worker-identity collisions: identical Service names in
two namespaces produce two ConfigMaps, and their combined registrations MUST
fail `plan.Build` as duplicate identities.

## Verified lifecycle and permissions

The pinned chart is 3.9.2, controllers and CLI are 1.19.2. Actual local controller
checks prove generation on create, synchronized dependency update, cleanup on
source delete, false flag and annotation removal, re-enable, recreation with a
new UID, and `generateExisting` reconciliation after policy installation.
Background reports identify preexisting violations. Pods, Deployments,
unregistered Services and unselected namespaces do not generate registrations.
Tests use bounded convergence plus stable absence checks and positive generation
controls. No supplementary cleanup policy or source-validation fallback is used.

These are distinct native paths in tagged v1.19.2:
`pkg/webhooks/resource/generation/handler.go` (`syncTriggerAction`) handles trigger
deletion and losing a match; `pkg/background/generate` processes downstream
cleanup; `pkg/policy/generate.go` handles existing sources. Native behavior was
accepted from real API outcomes, not inferred from `synchronize` or readiness.
The ClusterPolicy API is supported but deprecated in this pinned release;
migration to CEL policy kinds is future work, not an implicit version upgrade.

`rbac.yaml` adds **only** Service get/list/watch, aggregated into Kyverno admission,
background and reports controller roles. Cluster-wide reads are necessary because
v1.19.2 `pkg/policy/policy_controller.go:getResources` lists ClusterPolicy triggers
across namespaces before evaluating namespace selectors; reporting also uses
cluster-wide informers. Namespace labels are selection, not authorization.
The already-installed pinned chart grants background ConfigMap CRUD cluster-wide;
this task adds no write privileges and does not alter that shared baseline.
A hardened installation must explicitly provide background ConfigMap CRUD in
each worker namespace (for example namespace RoleBindings to a ConfigMap-only
role) if those chart defaults are removed. Adding a namespace label alone cannot
replace those grants. No orchestrator or Secret permissions are added.

Re-run `policies:install` after namespace/RBAC setup, and run the live lifecycle
test on the exact pinned controller build. These tests do not certify cleanup
across controller downtime, policy drift, or arbitrary version upgrades. Local
macOS/arm64 proof is not a substitute for the controller-owned Linux CI run.
