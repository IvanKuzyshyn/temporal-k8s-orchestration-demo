# Minimal full-demo scope

## Intent and approvals

The project supports one local demonstration: `mise install`, `mise run up`, a body-free `POST http://127.0.0.1:18080/runs`, `mise run down`, and `mise run destroy`. The user approved removing Compose, static/always-running worker modes, staged deployment/rollback experiments, advanced fault injection, duplicate bootstrap inputs, redundant lifecycle commands, and overlapping tests. Idempotency must remain. The user specifically clarified that `cmd/validate-graph` is removed, **not CI/offline graph validation**.

This document records that agreement and the implementation choices below; it is not authorization to delete an existing local cluster.

## Supported architecture

- One dedicated kind cluster: `temporal-demo`, context `kind-temporal-demo`, checkout-local `.local/kubeconfig`.
- PostgreSQL/PVC/immutable credentials, Temporal namespace `default`, Temporal UI.
- One always-running orchestrator; four workers in separate namespaces.
- Canonical diamond: worker-a → worker-b/worker-c → worker-d.
- Kyverno generates registrations from worker Service annotations. The chart creates Deployments, Services, discovery RBAC and KEDA ScaledObjects. Kyverno does not provision Deployments or scalers.
- Orchestrator reads the live Kubernetes registry and starts identity-only parent/child workflows.
- KEDA owns worker replicas: min/max 0/1, polling 5s, cooldown 120s, both workflow and activity queues. No optional running-workflow-count experiment.
- Keep activity retries, heartbeat, cancellation and immutable plan snapshots; these are correctness mechanisms, not alternate demo modes.
- Preserve existing tool/image/chart pins; no platform migration or dependency upgrade.

## HTTP contract

Keep `POST /runs`, `GET /runs/{workflowID}`, `/healthz`, `/readyz`. Replace `/workers` with `GET /graph` (no final compatibility alias).

`GET /graph` is read-only, body-free, and returns `{synced, valid, plan, problems}`. `plan` uses the existing canonical Plan shape. `problems` is a JSON array, including an empty array on success.

- 200: synchronized, nonempty, canonical valid graph.
- 422: synchronized but invalid/empty/noncanonical graph.
- 503: discovery unsynchronized; this takes precedence over invalidity.

The endpoint validates one copied discovery snapshot through the shared graph authority; it does not create workflows, edit registrations, accept arbitrary manifests, or make Temporal calls. It does not require clients to validate before POST. `/readyz` continues to represent synchronization/polling/Temporal connectivity, not graph validity.

Retain the complete existing idempotency contract: printable key validation, deterministic keyed IDs, random unkeyed IDs, durable original identifiers/hash/result, duplicate race handling, ambiguity-safe 503, and replay after restart or graph changes. Existing keyed runs resolve before consulting the current graph, even when graph validation returns 422/503. Retention remains the deduplication boundary.

## Lifecycle and validation

`up` creates/bootstrap-configures a fresh owned cluster or resumes the existing owned installation without rebuilding/redeploying it. It exposes localhost API/UI and succeeds only when the full intended graph, scalers and HTTP endpoints are ready. `down` stops owned tunnels and the node, preserving storage/history. `destroy` deletes the verified owned cluster and lifecycle records. Active work can be interrupted by down and deadlines continue to elapse.

Retain container identity, kubeconfig scope, PID/start-time verification, lock, occupied-port failure, bounded readiness, safe partial-bootstrap retry and KEDA replica-ownership handling. Do not automatically adopt legacy/unowned clusters or silently migrate existing installed binaries.

Remove the graph CLI and all runtime `go run ./cmd/validate-graph` calls. Keep `internal/plan` validation, `internal/manifest` extraction, chart graph tests, Helm/schema checks and strict Kubernetes server dry-run before apply.

### Agreed startup graph convergence

After infrastructure, Kyverno policies and the orchestrator API are ready, `up` polls `GET /graph` at one-second intervals for at most 120 seconds of elapsed time. Issue the first request immediately; requests never overlap. Bound each request to at most two seconds and the remaining overall deadline, and cap sleeps by the remaining deadline. Slow requests can reduce the number of polls: this is not a loop of 120 attempts and must not exceed the overall budget by repeatedly granting fresh request timeouts. Cluster creation, image builds/downloads and earlier readiness waits are outside this graph budget.

Succeed immediately on a 200 response with synchronized, valid discovery and the complete expected worker identities, queues, workflow types and dependencies. A valid partial graph or the right names with wrong dependencies is not success. Retry connection errors, transient HTTP errors, unsynchronized discovery and invalid/incomplete snapshots until the deadline. On timeout return nonzero and report the last HTTP/connection outcome, validation problems, and missing/extra/mismatched workers without dumping secrets. Two minutes is a reasonable default, not a startup guarantee on overloaded hosts.

Derive the expected workers/dependencies from the intended rendered deployment resources during bootstrap and retain that small expectation in checkout-local lifecycle state for resume. Do not add the previously proposed checked-in `k8s/expected-graph.json` or a startup hash comparison. Do not derive the expectation from `/graph` itself, or implement a second graph validator/hash algorithm in shell. Offline Go tests continue to validate the rendered graph with `manifest.Registrations` and `plan.Build`, and check the startup projection against their output.

The retained expectation is readiness bookkeeping only, never mounted in the orchestrator or used as a discovery fallback. Missing/malformed expectation state must fail with actionable diagnostics, not adopt the observed graph. Each new POST still reads/validates its own current snapshot; startup does not freeze the execution graph. Existing keyed requests resolve from Temporal history.

## Removal boundaries

Remove Compose runtime/CI/credentials/healthcheck binary and duplicated `deploy/temporal` inputs; keep the app Dockerfiles and Kubernetes bootstrap ConfigMaps. Remove production static provider/config, registry/scaling mode switches, fixed worker replica setting and alternate values overlays. Remove baseline/scaling/discovery/resilience experiment drivers, relay image, retained-stack restoration/ownership ledgers, staged CI and redundant public task entrypoints. No lifecycle API for changing the graph is introduced.

Keep dependency-graph algorithms and discovery reconnect/relist/permission/copy-safety tests. Do not broaden RBAC to simplify implementation. Keep chart security settings, credential protection and secret-safe failure diagnostics. Restrict simulated activity duration to the deployed five-second demo; test-only duration injection can remain internal.

## Test strategy

- Fast suite: plan/manifest, discovery, workflows/activities, HTTP, idempotency/Temporal gateway, lifecycle boundary tests, full-chart/security/schema tests and policy tests.
- One real disposable-cluster integration scenario exercises the public lifecycle, zero-to-running-to-zero diamond, generated registrations, essential RBAC, keyed replay, stop/resume retained history, another run, and destroy.
- Use existing history-order assertions where valuable, but not their staged deployment or rollback harness.
- Keep unique idempotency cases at the service layer, HTTP-specific edge cases at the handler layer, SDK policy/Memo cases at the gateway layer, and one real durable replay case. Do not delete cases merely because their filenames or subjects overlap.
- One validation CI job and one full-demo integration job; do not repeat all validation inside the integration job. Retain bounded waits and scoped failure diagnostics.

## Non-goals and migration

No Compose, fixed-worker fallback, registry mode selection, optional scale-signal experiments, chaos suite, alternate graphs as deployment modes, or production hardening project. Do not replace Helm/client-go/Temporal abstractions just to reduce line count.

Existing PR #14 installations contain old application images and `up` intentionally does not redeploy. Document deliberate `destroy`/`up` to install this changed API (history loss, explicit operator decision). Normal down/up within the new version preserves data. Do not claim automatic in-place upgrade support.
