# Temporal orchestration demo

Go + Temporal dependency-aware workflows: `worker-a → worker-b/worker-c → worker-d`.
A body-free HTTP request snapshots a validated static graph; each worker returns
only its identity. Compose runs four always-on workers; **it does not emulate
KEDA or Kyverno**. The kind baseline runs the same static graph across five
application namespaces. Controllers are installed, but discovery, policies and
zero-to-one scaling remain later stages.

## Prerequisites

- Docker Engine/Desktop with Compose v2+ (`--wait`, health/dependency conditions),
  Linux containers, roughly 8 GB available memory and 4+ CPUs recommended.
- [mise](https://mise.jdx.dev/), Git, Bash, curl, jq and OpenSSL on the host.
- `mise install` installs pinned Go1.27.3, kind0.33.0, kubectl1.35.8,
  Helm4.3.1 and kubeconform0.8.0, without changing global tool configuration.
  Race tests need a C compiler
  (Xcode command-line tools on macOS; GCC on Linux).

Images are pinned by release **and multi-platform digest**. Go build/runtime,
PostgreSQL16.15, Temporal server/admin-tools1.32.0, and independently pinned
UI2.48.0 have Linux ARM64 and amd64 manifests. Darwin ARM64 uses Docker's
Linux ARM64 VM, not Darwin container images. See [versions/evidence](docs/versions.md).
No SQLite, Temporal dev server, hosted Temporal, or auto-setup image is used.

## Quick start

```sh
mise install
mise run validate              # Go/race/vet, Helm, full graph, strict schemas, Compose config
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

## Isolated kind baseline quick start

Leave an existing Compose stack or unrelated kind cluster running; these tasks
never invoke Compose up/down/reset or change global Kubernetes/Docker contexts.
From **this checkout**, with Docker running:

```sh
mise install
mise run validate:manifests     # no cluster: Helm lint/render, complete graph, strict schemas
mise run k8s:up                # create if absent, apply infrastructure, build/load/validate/apply apps
mise run test:kind-baseline     # diamond + Kubernetes-only orchestrator restart/keyed replay
mise run test:kind-persistence  # replace Temporal/PG Pods + safe schema rerun, retain history
mise run validate              # same cumulative command used by all PR workflows
```

Run mutating tasks **sequentially**, especially `k8s:up` and persistence:
they share the schema Job. Static workers remain at one replica. Namespace
`temporal-demo-orchestrator` holds the API/parent poller. Each of `worker-a`
through `worker-d` has its own `temporal-demo-worker-<letter>` namespace labeled
`temporal-demo.io/worker-namespace: "true"`. Values in `k8s/worker-helm-chart/values.yaml`
define each identity/dependency once; Namespace names, Service annotations,
Deployment queues and the static registry are generated together. No discovery
RBAC, Kyverno policies/registrations or KEDA ScaledObjects are enabled. Temporal
uses `temporal.temporal.svc.cluster.local:7233` across namespaces, namespace
`default`, with separate PostgreSQL default and visibility databases.

The kubeconfig is **ignored and worktree-local**: `.local/kubeconfig`.
Every kubectl operation selects `kind-temporal-demo`; creation/deletion explicitly
selects `temporal-demo` and the local kubeconfig. No `use-context` or global
kubeconfig write occurs. Tasks reject a foreign `KUBECONFIG`; unset it first,
or set it to this checkout's absolute `.local/kubeconfig`. An existing cluster
requires that file's current context to match. Do not copy kubeconfigs between
checkouts or adopt another cluster. There is no UID/Docker-ownership framework.
Manual reads:

```sh
KUBECONFIG="$PWD/.local/kubeconfig" mise exec -- kubectl --context kind-temporal-demo get pods -A
mise run kind:diagnostics       # scoped status/app logs; failure-only credential-redacted schema evidence
```

Infrastructure inputs live in `k8s/infrastructure/`:
`kind.yaml` is **kind input, never an apply resource**; `temporal.yaml` contains
PostgreSQL/PVC, config/scripts, schema and namespace Jobs, server and UI;
`keda.yaml` contains complete pinned controllers/CRDs/RBAC. Kyverno's pinned
chart is downloaded only on cache miss, SHA256-verified on **every** use, then
rendered with `kyverno-values.yaml` and strictly validated before applying the
same temporary manifest. Cold archive/schema caches require network access;
ordinary validation needs no live cluster.
`k8s-up.sh` applies labeled phases: PostgreSQL readiness → completed schemas →
Temporal readiness → completed default-namespace bootstrap → UI; then controller
CRDs (server-side apply), establishment and controllers/webhook CAs. Bootstrap
Jobs are recreated on reruns, not databases/storage. Both images are built directly
with Docker, tagged with the source revision (plus a unique dirty suffix), loaded
into kind, and the **same rendered bytes** pass complete-graph/strict-schema
validation before kubectl apply. Helm only renders; it no longer installs releases.
See [render provenance and reproduction](k8s/infrastructure/README.md), including
migration from the prior Helm-managed baseline and excluded Helm lifecycle hooks.

Forwarding is localhost-only and distinct from Compose's occupied ports:

```sh
mise run api:forward            # foreground, http://127.0.0.1:18080; Ctrl-C stops only this tunnel
mise run ui:forward             # separate terminal, http://127.0.0.1:18088
# API_FORWARD_PORT=19080 mise run api:forward  # override when needed
# UI_FORWARD_PORT=19088 mise run ui:forward
curl -fsS -X POST -H 'Idempotency-Key: kind-demo' http://127.0.0.1:18080/runs
```

Integration tasks allocate their own available localhost API port atomically and
clean up their exact child PID and temporary files on success/failure/signals.
No manually running tunnel is required; no process is killed by name/pattern.

### kind persistence and deletion warning

`k8s:up` reruns reuse the existing **immutable**
`temporal/postgres-credentials` Secret. The StatefulSet's `data-postgres-0` PVC
must stay paired with that Secret. A retained PVC without its Secret fails closed
instead of generating a new unusable password. Never delete/rotate that Secret
behind retained data. Local passwords are not committed or printed. Schema Jobs
use the same pinned1.32.0 tools/scripts as Compose, safely initializing/updating
both databases without overwrite/drop. Readiness and bootstrap are bounded.

Ordinary Pod replacement and schema reruns preserve exact completed workflow ID,
run ID, plan hash and identity-only result. The automated persistence test checks
different Pod UIDs and unchanged PVC UID/bound volume plus Secret UID/version.
This is local PVC persistence, **not host-external backup or production HA**.

```sh
mise run k8s:down               # DESTRUCTIVE: ONLY temporal-demo, including ALL its PVC history
# To reset deliberately: down, then k8s:up. Old histories will be gone.
```

Deletion is explicit and idempotent; integration tests leave the cluster/history
available for inspection. Only disposable Linux CI exercises down/down/up and
checks old history returns404, followed by a fresh diamond. Local retained history
is not reset. CI unconditionally attempts scoped deletion; Validate and Compose
coverage remains.
Darwin ARM64 Docker acceptance and Linux amd64 CI acceptance are separate gates;
see [versions/evidence](docs/versions.md). Controller installation/readiness is
**not** proof of KEDA no-poller scaling or Kyverno admission/registration cleanup.

## Complete rendered worker graph validation

```sh
mise exec -- go run ./cmd/validate-graph < rendered.yaml
mise run test:manifests         # extraction tests + real CLI fixture exit/output assertions
```

Provide the **complete** multi-document YAML/JSON deployment set, including
Namespace declarations; Kubernetes `List` items (also nested Lists) are expanded.
Namespaces labeled `temporal-demo.io/worker-namespace: "true"` select graph scope.
Services registered with annotation `temporal-worker: "true"` require an explicit,
declared namespace; registrations outside labeled namespaces are ignored. Queues
and workflow types are derived from Service identity, and the unchanged
`temporal-worker-run-after` annotation uses the shared dependency parser.

The CLI prints only the canonical plan hash on success, or a diagnostic to stderr
and exit1 on invalid input. Duplicate Namespace or Service definitions are
ambiguous and rejected, even if identical or disabled. Shared graph validation
rejects unknown/self/cyclic dependencies, globally duplicate worker identities
and multiple workers in one namespace. This is **graph/source validation**, not a
general Kubernetes/CRD schema check, admission test, or proof of runnable workers.
No cluster or Docker runtime is needed. `mise run validate:manifests` additionally
runs actual Helm lint/render, the CLI over the **complete** application render,
and kubeconform strict Kubernetes1.35.8 plus release-pinned custom schemas.
Unknown kinds fail (a negative fixture proves it); no missing-schema skip is used.
Custom-schema fixtures are validation-only, never deployed. Core schemas use an
immutable upstream revision; selected CRD schemas are extracted from exact
hash-verified chart releases; see [schema provenance](k8s/schemas/README.md).
These checks are included additively in cumulative `mise run validate`.

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
