# HTTP API

`mise run up` exposes the unauthenticated, localhost-only API at
`http://127.0.0.1:18080` and Temporal UI at `http://127.0.0.1:18088`.
No public ingress is configured. Keep tunnels bound to localhost.

## POST /runs

```sh
curl -fsS -X POST http://127.0.0.1:18080/runs
# Optional retry-safe key:
curl -fsS -X POST -H 'Idempotency-Key: my-run' http://127.0.0.1:18080/runs
```

Send zero body bytes. `{}`, whitespace, unknown-length/chunked body bytes and body
read errors return 400. No Content-Type is needed. The handler probes at most one
byte rather than buffering unexpected input.

A successful response is HTTP 202 with `workflowId`, `runId`, `planHash`, and
`status` (normally `running` for a new run). No business data is accepted or passed
to workflows. New starts require synchronized, nonempty, valid discovery. Each
start validates and copies the current plan; later registration changes cannot
mutate an existing workflow's input.

### Idempotency

An optional Idempotency-Key must have exactly one value, 1–128 printable ASCII
bytes (0x20–0x7e). Present-but-empty is invalid. Application-visible spaces are
significant; HTTP transports can normalize surrounding header whitespace.

Keyed IDs are `run-` plus lowercase SHA-256 of the key bytes. Unkeyed requests get
cryptographically random IDs. Temporal is the source of truth, not an in-memory
map: duplicate workflow IDs are rejected and lookup/start races resolve against
the durable execution. Memo retains the original canonical `planHash`.

The same key returns 202 with the original workflow/run/hash and current status
or result, including after completion, process restart, stop/resume or registry
changes. Existing keyed runs are looked up **before** graph validation; graph
422/503 does not by itself block replay. New keys select the current valid plan.
Missing/corrupt durable metadata is an error, never fabricated successful replay.

A request has a two-second operation budget, shortened by its request deadline.
503 or a lost response can happen **after Temporal accepted the workflow**.
Retry the same key; an unkeyed retry can create extra work. Keys are not logged or
included in error text. Deduplication lasts only within retained Temporal history
(configured retention 72 hours); destroying storage destroys that history.

## GET /runs/{workflowID}

```sh
workflow_id='paste-workflowId-here'
curl -fsS "http://127.0.0.1:18080/runs/$workflow_id"
```

Returns 200 with workflowId, runId, planHash and status. Statuses: `running`,
`completed`, `failed`, `canceled`, `terminated`, `timedout`. Retrieval does not wait
for a running workflow. Completed results are pinned to the described run ID,
with following of later runs disabled. A completed diamond result is:

```json
{"results":[{"workerName":"worker-a"},{"workerName":"worker-b"},{"workerName":"worker-c"},{"workerName":"worker-d"}]}
```

Non-success terminal states include a safe failure summary, not exception causes
or payloads. Unknown/expired execution: 404 `{"error":"run not found"}`.

## GET /graph

```sh
curl -fsS http://127.0.0.1:18080/graph
# To inspect diagnostics even when validation fails:
curl -sS http://127.0.0.1:18080/graph | jq
```

Read-only validation of one current registry snapshot, without Temporal calls,
workflow creation or registration edits. Response fields:

- `synced`: whether discovery has synchronized.
- `valid`: whether the snapshot is nonempty, valid and canonical.
- `plan`: schemaVersion, canonical nodes and hash. Each node includes name,
  taskQueue, workflowType and dependencies (roots serialize dependencies as null).
- `problems`: diagnostics array, empty on success.

HTTP statuses: **200** synchronized/valid; **422** synchronized but invalid or
empty; **503** unsynchronized (takes precedence over invalidity). `/workers` has
been removed, with no compatibility alias.

`up` polls this endpoint every second for up to 120 elapsed seconds after earlier
infrastructure/API readiness. It also compares the full expected node set and
dependencies derived from the installed deployment, because a valid partial graph
is not complete startup. Requests are nonoverlapping, capped at two seconds and
the remaining deadline. Startup expectations never become execution inputs.
POST always reads its own current snapshot; a previous GET is not required and
cannot reserve or freeze a graph version.

## Health

- `GET /healthz`: process liveness, independent of registry/Temporal availability.
- `GET /readyz`: discovery synchronized, SDK polling initialized and Temporal
  connectivity available within two seconds (or the shorter request deadline).
  A synchronized-invalid graph can be ready for diagnosis but reject new starts.

## Safe errors

Start/status errors use a single `error` field:

- 400 `malformed request`: unexpected body or malformed idempotency header.
- 503 `service unavailable`: unavailable graph/Temporal, failed duplicate lookup
  or ambiguous timeout; never proof that execution was not accepted.
- 404 `run not found`: unknown execution on GET.
- 500 `internal error`: unexpected failures, including invalid durable metadata.

Graph validation uses its structured diagnostic response, not the run error
schema. Internal exceptions and credentials are never returned as run errors.
