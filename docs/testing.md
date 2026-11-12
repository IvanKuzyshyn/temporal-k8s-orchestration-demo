# Test ownership

The repository tests one runtime: kind with live Kyverno discovery and KEDA scaling.

## Fast checks

`mise run validate` runs formatting, the race-enabled Go suite, vet, shell syntax,
policy checks through the Go tests, and Helm/schema rendering. No live cluster is
needed. Initial controller chart downloads require network access.

- `internal/plan`: dependency parsing, invalid graphs, canonical ordering/hash.
- `internal/manifest`: graph extraction from actual Kubernetes YAML resources.
- `internal/chart/full_test.go`: the single full chart in bootstrap/steady states;
  identities, namespace counts, scoped RBAC, Pod security/probes, exact scaler
  queues/thresholds, zero bootstrap and replica-free steady state.
- `internal/chart/graph_test.go`: projection agrees with the Go graph; graph
  polling handles missing/mismatched workers and transient errors within its
  one-second cadence and 120-second budget.
- `internal/chart/local*_test.go`, `lifecycle_test.go`, `demo_runner_test.go`:
  owned stop/resume/delete, PID safety, occupied ports, failed bootstrap and
  server validation, preserved credentials and native replica ownership.
- `internal/registry`: decoding, copy isolation, initial synchronization,
  reconnect/relist/list-attempt/namespace-view barriers and permission loss.
  These cover different race conditions, not interchangeable copies.
- `k8s/policies/tests`: actual pinned CLI CEL admission and generation contracts,
  with deprecation warnings treated as errors. Shared dependency parser fixtures,
  every Go whitespace character, optional annotations, scope exclusions, raw
  dependency preservation, and all registration data fields are exercised.
- Workflow/activity tests: diamond scheduling, input/result identities, retry,
  heartbeat, cancellation and misrouting behavior.

## Idempotency coverage retained

Service tests (`services/orchestrator/runs/service_test.go`) own detailed behavior:

- `TestDurableRetriesSurviveRegistryChangesAndRestart`: completed replay on a new
  service instance despite invalid/unsynchronized discovery; new key is rejected.
- `TestConcurrentSubmissionsAndAlreadyExistsRace`: simultaneous starts and forced
  lookup/start races against running and completed executions with changed plans.
- `TestAlreadyExistsLookupFailureNeverInventsAcceptance`,
  `TestAmbiguousStartIsUnavailableUntilDurableRetry`, `TestNoStartOnLookupFailure`:
  ambiguity and unavailability must not invent acceptance or additional work.
- `TestRetryKeepsOriginalValidPlanAndNewKeyUsesChangedPlan`: old key stays bound;
  a new key uses the changed graph.
- `TestStartsHaveIndependentInputsAndUniqueUnkeyedIDs`: unkeyed ID uniqueness and
  private plan inputs; `TestNewStartsRevalidateCopiedPlans`: admission validation.
- `TestServiceGetAndMalformedKeys`: service lookup and key contract.

Gateway tests (`runs/temporal_test.go`) keep SDK conflict/reuse options, Memo
validation, described-run-pinned results, corrupt metadata and exception mapping.

HTTP tests keep body-byte/chunked validation, header multiplicity/ASCII/length,
deadlines, HTTP codes and redaction. `TestHTTPRunJSONAndCompletedReplay` checks
serialization and status/replay routing without repeating service concurrency
loops. `TestHTTPAmbiguousStartStatusAndRecovery` checks the 503/202 transport
contract. `TestGraphInvalidityDoesNotBlockDurableReplay` connects graph 422/503 to
old-key replay and new-key rejection.

Removed HTTP concurrency/restart loops are covered by the named service tests,
not dropped guarantees. Static-registry, Compose, phased rollback and chaos-test
cases were removed with their unsupported runtimes, not replaced with weaker
versions of those runtimes.

## One real lifecycle scenario

`mise run test:integration` requires an unused `temporal-demo` cluster name and no
checkout-local lifecycle state. It **refuses** an existing cluster. Use disposable
CI or explicitly prepare a separate Docker environment; never reset a retained
user cluster to satisfy a test preflight.

The runner invokes the public `up`, then `TestDemoLifecycle` under the dedicated
`demo_integration` tag. Its `CELPolicyLifecycle` subtest checks live admission
(including invalid existing objects), source updates, generated-resource repair,
worker opt-out, source deletion/recreation and UID replacement. An isolated copy
of the generator tests existing-source generation and policy-deletion cleanup
without changing the demo policy. Fixture namespaces are removed before graph
assertions. It checks new policy readiness, absence of the legacy policy instances,
and no ClusterPolicy deprecation warning in fresh startup logs. A deliberate
`synchronize.enabled: false` mutation was verified to fail this subtest.

The remaining scenario checks generated registrations and essential RBAC,
zero real worker Pods, an unkeyed body-free POST, child histories and diamond
ordering, return to zero, completed keyed replay, repeated up/down, node stop,
retained PVC/credential identities and replay after resume, then another new run.
The runner destroys only its newly owned cluster and repeats destroy.

`TestDemoHistoryAssertionsSensitivity` verifies the history assertions reject
wrong queues, extra payload data, wrong dependency order, duplicate children and
missing results. These are migrated checks, not merely final-status assertions.

CI runs one fast Validate job and one disposable Kind integration job. The latter
does not rerun the entire fast suite. Lifecycle test failures retain safe console
diagnostics before cleanup; kubeconfigs and credential files are not uploaded.
