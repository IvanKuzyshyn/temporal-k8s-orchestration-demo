# Temporal orchestration demo

Go + Temporal dependency-aware workflows: `worker-a → worker-b/worker-c → worker-d`.
A body-free HTTP request snapshots a validated static graph; each worker returns
only its identity. Compose runs four always-on workers; **it does not emulate
KEDA or Kyverno**. Kubernetes discovery/scaling are later stages.

## Prerequisites

- Docker Engine/Desktop with Compose v2+ (`--wait`, health/dependency conditions),
  Linux containers, roughly 8 GB available memory and 4+ CPUs recommended.
- [mise](https://mise.jdx.dev/), Git, Bash, curl, jq and OpenSSL on the host.
- `mise install` installs pinned Go1.27.3. Race tests need a C compiler
  (Xcode command-line tools on macOS; GCC on Linux).

Images are pinned by release **and multi-platform digest**. Go build/runtime,
PostgreSQL16.15, Temporal server/admin-tools1.32.0, and independently pinned
UI2.48.0 have Linux ARM64 and amd64 manifests. Darwin ARM64 uses Docker's
Linux ARM64 VM, not Darwin container images. See [versions/evidence](docs/versions.md).
No SQLite, Temporal dev server, hosted Temporal, or auto-setup image is used.

## Quick start

```sh
mise install
mise run validate              # format, race tests, vet, credential-free Compose check
mise run compose:up            # builds BOTH app images, waits for all services
mise run compose:smoke         # real diamond + completed/restarted keyed replay + body400
mise run compose:persistence   # isolated restart/replacement/schema/down-up/reset proof
```

`compose:up` creates **ignored** `deploy/.env` with a random PostgreSQL password
and owner-only permissions if absent. Never commit it. `deploy/.env.example`
is a nonsecret template, not a working credential. To prepare your own file:
`(umask 077; printf 'POSTGRES_PASSWORD=%s\n' "$(openssl rand -hex 24)" > deploy/.env)`.
Keep the same password with an existing volume; changing it does not change an
already initialized PostgreSQL user's password. Do not print rendered Compose
configuration or infrastructure logs that might contain credentials.
`docker compose -f deploy/compose.yaml config --quiet` works without real creds;
the validation-only defaults are **not** the supported runtime startup path.

Local endpoints (published on **127.0.0.1 only**):
- API: <http://127.0.0.1:8080> (`/healthz`, `/readyz`, `/workers`, `/runs`).
- Temporal gRPC: `127.0.0.1:7233`.
- Temporal UI: <http://127.0.0.1:8088>.

```sh
curl -fsS -X POST -H 'Idempotency-Key: my-demo-run' http://127.0.0.1:8080/runs
# GET /runs/<workflowId> until status=completed; no body or Content-Type needed.
# Same key returns the original durable workflowId/runId/planHash/result.
```

Deduplication lasts within Temporal history retention (default namespace72h).
Unexpected body bytes return400. Results contain only sorted `workerName` values;
there is no business payload. See [API contract](docs/api.md).

## Lifecycle, history and destructive reset

```sh
mise run compose:down          # removes project containers/network; PRESERVES history
mise run compose:up            # retained history still queryable
RESET_CONFIRM=1 mise run compose:reset  # DESTRUCTIVE: removes project's PG volume/history
```

One-shot schema setup initializes/migrates **both** `temporal` and
`temporal_visibility` with matching1.32.0 `postgres12` schemas before server
startup. Setup safely reruns without overwriting existing versions/history.
Namespace bootstrap checks server health and creates/checks `default` before
application pollers start. PostgreSQL uses a project-scoped named volume.
This is local single-instance persistence, **not** a production HA/backup solution.

`compose:persistence` uses a fresh unique project, random localhost ports and its
own temporary credential file/volume. It proves restart, container replacement,
schema rerun and down/up preserve exact workflow/run/result, then invokes the
explicit reset task **only on that fixture**, expects old history404, and completes
a fresh run. Its trap removes its containers/volume even on assertion failure.
It never resets the ordinary demo. Leave ordinary `deploy/.env` and its volume
alone unless intentionally resetting. No global Docker prune/context changes.

Change host ports via `COMPOSE_API_PORT`, `COMPOSE_TEMPORAL_PORT`,
`COMPOSE_UI_PORT`; set `COMPOSE_API_URL` for smoke if changing API port.
`COMPOSE_PROJECT_NAME` isolates multiple demos; `COMPOSE_ENV_FILE` selects a
separate local credential file. Don't reuse another project's name.

## Processes, builds and checks

- `mise run images:build`: builds nonroot orchestrator and shared worker images.
  Clean source uses the full Git SHA; dirty builds get a unique explicit
  `local-<timestamp>-<random>` tag, recorded in ignored `.local/compose-image-tag`.
  `compose:up` rebuilds (with Docker cache) so edits cannot silently use a stale
  SHA or `latest`. Build contexts exclude secrets/local state.
- `mise run compose:config`: configuration validation without rendered secrets.
- `bash scripts/compose.sh diagnostics`: bounded project-scoped status and only
  application logs, no environment dumps or raw infrastructure logs.
- PR workflows reuse the local setup-mise action and these same tasks on Linux
  amd64. `mise run compose:cleanup` requires an explicit
  `COMPOSE_PROJECT_NAME=temporal-demo-ci-<run-id>-<attempt>`; it checks fixture
  discovery, attempts every matching fixture and the job's normal project even
  after failures, and returns an aggregate failure. It never defaults to resetting
  the ordinary demo. CI evidence is distinct from local host evidence; see versions.md.

Smoke readiness/completion each have an elapsed `SMOKE_WAIT_SECONDS` budget
(default180), not180 attempts. Requests are capped at the remaining budget (at
most5s), with at most one second of shell-clock granularity; failure diagnostics
run afterward. Slow or unavailable endpoints cannot multiply the budget by the
per-request timeout.

`services/orchestrator/cmd/orchestrator` wires chi, static registry and parent
poller. `services/worker/cmd/worker` registers the fixed child workflow and
identity-configured activity. Environment: `TEMPORAL_ADDRESS` (default
`temporal:7233`), `TEMPORAL_NAMESPACE` (`default`), `REGISTRY_MODE` (`static`),
`REGISTRY_FILE` (`deploy/config/workers.json`), `WORKER_NAME`, `TASK_QUEUE`,
`ACTIVITY_DURATION` (`5s`, positive and less than60s), `HTTP_ADDR` (orchestrator
`:8080`, worker `:8081`). Queues must match `<worker>.v1.temporal` or the parent
`orchestrator.v1.temporal`. `WORKER_NAMESPACE_SELECTOR` is reserved; selecting
`REGISTRY_MODE=kubernetes` fails explicitly until Task9 rather than pretending
that static registration is dynamic discovery. No registry watchers here.

Liveness is independent of Temporal; readiness requires SDK worker Start/polling
and a bounded Temporal health RPC. Startup connection retries are bounded to90s
and obey cancellation. SIGTERM marks unready and grants SDK worker Stop30s;
Compose grace40s includes HTTP shutdown. Logs use JSON slog through the SDK
adapter, including queue and activity/workflow/run IDs. SDK fatal errors atomically
mark the process unready and block new starts, including a fatal racing startup;
the process joins SDK shutdown outside the callback and returns the fatal cause.
