# Orchestrator HTTP API

Task 3 provides a chi handler and injectable Temporal gateway; executable server/configuration wiring belongs to Task 4. These examples describe that handler's contract, not an already running stack. The eventual local API address is `http://127.0.0.1:8080`.

## Start a run

```sh
curl -i http://127.0.0.1:8080/runs \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: key' \
  --data '{"message":"hello"}'
```

The body must contain exactly one object with exactly one `message` string. Empty strings are valid; missing/null messages, unknown or duplicate fields, wrong casing, trailing JSON, malformed JSON and invalid UTF-8 are rejected. Whitespace surrounding the object is permitted. The entire body, **including trailing whitespace**, must be at most 16 KiB (16,384 bytes).

`Idempotency-Key` is optional. If supplied it must have exactly one value of 1–128 printable ASCII characters (bytes 0x20–0x7e); present-but-empty is invalid. Application-visible spaces are significant, never trimmed by the handler. HTTP transports may normalize surrounding header whitespace before handing it to the application. Keys are opaque and are never included in application logs or response errors. Without a key, every request receives a cryptographically random workflow ID.

Example HTTP **202** response for the checked-in diamond registry (the run ID below is illustrative and allocated by Temporal):

```json
{"workflowId":"run-2c70e12b7a0646f92279f427c7b38e7334d8e5389cff167a1dc30e73f826b683","runId":"5e6b7b18-cc5b-4d20-b86e-88af2b1b29d4","planHash":"5fefcdcc52af25c9c21a523f1f3c1f7d928018051ffe89ac8b5a0e6ccf8c3b62","status":"running"}
```

### Durable idempotency

A keyed workflow ID is `run-` followed by the lowercase SHA-256 hex digest of the key bytes. Temporal is the source of truth, not an in-memory map. The gateway uses SDK reject-duplicate workflow ID reuse, conflict-fail semantics, and explicit already-started errors. Concurrent lookup/start races are resolved by describing the durable execution after an already-started error.

At creation, Temporal Memo stores separate `requestHash` and `planHash` strings. The request hash is SHA-256 over Go's compact JSON encoding of `contracts.Payload` (for hello, `{"message":"hello"}` hashes to `9b2d43affbf49a367028df2e1414f84c0e099ac98c3d54a8a80157fd7771af25`). It excludes registry data. The plan hash is the shared canonical execution-plan hash. Hashes and execution identifiers are decoded and validated on durable reads; missing/corrupt metadata is an internal error, not a successful repeat.

An equal-payload retry returns **202**, the **original** workflow ID/run ID/plan hash, and the current status/result if available, even after completion, orchestrator restart or registry changes. Existing keyed runs are looked up **before** validating the current registry, so a currently invalid or unsynchronized graph does not prevent their resolution. Reusing a key with a different payload returns **409**:

```json
{"error":"idempotency conflict"}
```

New starts require a synchronized, nonempty, valid registry. The service copies the plan, rebuilds/validates execution identities, queues, workflow types, dependencies and cycles through the shared graph builder, and verifies the canonical plan/hash before starting. It never changes provider data. Parent execution starts use `ParentWorkflow`, queue `orchestrator.v1.temporal`, a 30-minute execution timeout, immutable input and **no workflow retry policy**.

Deduplication lasts only while Temporal retains the execution history. Once retention expires or history is explicitly deleted, the key can create a new execution. There is no separate application idempotency database: durable metadata lives in Temporal's PostgreSQL-backed persistence when the runtime stack is deployed. These unit tests do not prove that infrastructure's persistence behavior.

A failed lookup or ambiguous start timeout returns **503**, never an invented acceptance. A timeout may occur after Temporal accepted the workflow. Retry with the **same key and payload** to discover the durable execution; an unkeyed retry may start additional work.

## Inspect a run

```sh
curl http://127.0.0.1:8080/runs/run-2c70e12b7a0646f92279f427c7b38e7334d8e5389cff167a1dc30e73f826b683
```

HTTP **200** includes `workflowId`, `runId`, `planHash` and `status`. Status values are `running`, `completed`, `failed`, `canceled`, `terminated`, and `timedout`. Retrieval never waits for an unfinished workflow to finish. A completed run retrieves the result for the **described run ID**, with SDK following of later runs disabled. A completed example:

```json
{"workflowId":"run-2c70e12b7a0646f92279f427c7b38e7334d8e5389cff167a1dc30e73f826b683","runId":"5e6b7b18-cc5b-4d20-b86e-88af2b1b29d4","planHash":"5fefcdcc52af25c9c21a523f1f3c1f7d928018051ffe89ac8b5a0e6ccf8c3b62","status":"completed","result":{"results":[{"workerName":"worker-a","message":"hello"},{"workerName":"worker-b","message":"hello"},{"workerName":"worker-c","message":"hello"},{"workerName":"worker-d","message":"hello"}]}}
```

Terminal non-success states include a safe `failure` summary such as `"Workflow failed"`, not internal exception causes, stack traces or payloads. An unknown/expired workflow returns **404** with `{"error":"run not found"}`.

## Diagnostics and health

- `GET /workers` always returns **200** with the provider snapshot: `synced`, `plan` (including `schemaVersion`, canonical `nodes`, and `hash`), and `problems`. Invalid/unsynchronized snapshots remain visible for diagnosis; they do not become runnable.
- `GET /healthz` returns **200** and `{"status":"ok"}` for process liveness, independent of registry or Temporal availability.
- `GET /readyz` requires initialized registry synchronization and successful supplied Temporal connectivity checking under a **2-second context deadline**, or a shorter request deadline. The supplied callback must honor context cancellation. Graph validity is deliberately not a readiness condition. Success is **200** with `{"status":"ready"}`; failure is **503** with `{"error":"not ready"}`. A missing ready function is not ready.

## Error responses

Handler errors are JSON with a single `error` field and no sensitive internal details:

- **400**: `{"error":"malformed request"}` — malformed payload or header.
- **413**: `{"error":"request too large"}` — body exceeds 16 KiB.
- **409**: `{"error":"idempotency conflict"}` — different payload for an existing key.
- **503**: `{"error":"service unavailable"}` — new start with invalid/unsynchronized registry, unavailable Temporal, failed duplicate resolution, or ambiguous timeout.
- **404**: `{"error":"run not found"}` — unknown run on GET.
- **500**: `{"error":"internal error"}` — unexpected internal errors, including invalid durable metadata. No raw downstream error text is exposed.

No authentication/public ingress or runtime configuration is introduced by this handler. Keep demo endpoints bound/forwarded to localhost when process wiring is added.

## Local verification

```sh
go test ./services/orchestrator/api ./services/orchestrator/runs
go test -race ./services/orchestrator/...
mise run validate
```

Tests use scoped Gomega assertions with standard `testing.T`, real handlers/services, and narrow fakes only at registry/Temporal boundaries. No live Temporal server or Kubernetes cluster is required. Existing cumulative CI runs all Go packages via the unchanged shared mise setup and `mise run validate`.
