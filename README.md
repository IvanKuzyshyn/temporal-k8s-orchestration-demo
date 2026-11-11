# Temporal Kubernetes orchestration demo

A body-free HTTP request snapshots a validated diamond graph:
`worker-a → worker-b/worker-c → worker-d`. Temporal runs the parent and children;
each child/activity carries **only worker identity**, not business data.
Compose is the fast static, always-on development path. Kind is the integration
path for native KEDA scale-to-zero and Kyverno registration discovery.

## Prerequisites

- Git, Bash, curl, jq, OpenSSL, [mise](https://mise.jdx.dev/), and Docker
  Engine/Desktop with Linux containers and Compose v2+ supporting `--wait`.
- A C compiler for Go race tests (Xcode command-line tools on macOS, GCC on Linux).
- `mise install` installs Go **1.27.3**, kind **0.33.0**, kubectl **1.35.8**,
  Helm **4.3.1**, and Kyverno CLI **1.19.2**. Use `mise run`/`mise exec`, not
  whichever versions happen to be on the global PATH.
- Network access for initial tools, Go modules, digest-pinned images and
  checksum-pinned chart archives. Cached validation needs no live cluster or
  Docker daemon; cold-cache validation is not network-offline.
- Roughly 4–6 available CPUs and 8–12 GB container-runtime memory is a starting
  resource estimate, **not** a measured minimum or startup guarantee.

Tested host/container architectures, exact pins and evidence provenance are in
[versions](docs/versions.md). Darwin ARM64 uses Linux ARM64 Docker containers.
Linux amd64 hosted CI is a separate gate; other platforms are not certified.
This is a localhost demo without production authentication, HA or external backup.

## Compose quick start — no Kubernetes

From a clean checkout with Docker running:

```sh
mise install
mise run validate              # formatting, race tests, vet, policies, chart/graph, Compose config
mise run compose:up            # builds both app images and waits for readiness
mise run compose:smoke         # real diamond, zero-body contract and durable keyed replay
mise run compose:persistence   # isolated project: restarts/replacement/schema/reset
```

Default localhost endpoints: API <http://127.0.0.1:8080>, Temporal gRPC
`127.0.0.1:7233`, UI <http://127.0.0.1:8088>. Compose does **not** emulate
KEDA/Kyverno: all four workers keep running with a static registry.

```sh
curl -fsS -X POST -H 'Idempotency-Key: my-demo-run' http://127.0.0.1:8080/runs
# Copy workflowId from the response, then poll:
workflow_id='paste-workflowId-here'
curl -fsS "http://127.0.0.1:8080/runs/$workflow_id"
curl -fsS http://127.0.0.1:8080/workers
```

Send **zero body bytes**, not `{}` or whitespace. No Content-Type is needed.
The same key returns the original workflow/run/hash and available result, even
following completion, API restart or registry changes. A 503/lost response can
follow Temporal acceptance: retry the **same key**, not a fresh key. Unkeyed
retries can create additional work. Deduplication lasts only within retained
Temporal history (default namespace retention 72 hours). See [API](docs/api.md).

`compose:up` creates ignored, owner-only `deploy/.env` with a random PostgreSQL
password if absent. Keep it paired with the project volume; do not source it,
print it, rotate it behind retained data, or commit it. Configuration checks use
`compose:config` (`config --quiet`), not credential-rendering `config` output.

### Separate project or occupied ports

Choose a previously unused project name and its own credential file. Never reuse
another checkout's retained project/credentials for a clean test.

```sh
export COMPOSE_PROJECT_NAME=temporal-demo-my-test
export COMPOSE_ENV_FILE="$PWD/.local/my-test.env"
export COMPOSE_API_PORT=0 COMPOSE_TEMPORAL_PORT=0 COMPOSE_UI_PORT=0
mise run compose:up
export COMPOSE_API_URL="http://$(mise exec -- bash scripts/compose.sh api-address)"
mise run compose:smoke
```

Port 0 allocates available **127.0.0.1** ports; export the API URL **after up and
before smoke**. For UI/gRPC, use `docker compose --env-file "$COMPOSE_ENV_FILE"
-p "$COMPOSE_PROJECT_NAME" -f deploy/compose.yaml port ui 8080` (or `temporal
7233`). Keep these environment variables for all operations on that project.

```sh
mise run compose:down          # containers/network removed; history volume retained
mise run compose:up            # same credentials + volume preserve history
RESET_CONFIRM=1 mise run compose:reset  # DESTRUCTIVE: this project's history volume
```

`compose:persistence` creates/removes its own unique child project, credentials
and volume; its destructive reset never targets the parent or ordinary demo.
Do not use global Docker prune. `compose:cleanup` is CI-only and requires its
exact `temporal-demo-ci-<run-id>-<attempt>` identity.

## Kind quick start — fixed replicas, static registry first

The named cluster is **temporal-demo**, not an arbitrary current context.
Leave unrelated clusters and Compose projects alone. Unset a foreign
`KUBECONFIG`; tasks use only this checkout's ignored `.local/kubeconfig` and
explicit context `kind-temporal-demo`, without changing global contexts.
An existing cluster requires the matching worktree-local file; do not silently
adopt another cluster or copy an unverified kubeconfig.

```sh
mise install
mise run validate:manifests    # cluster-independent Helm/values-schema/full graph checks
mise run k8s:up                # create if absent; infrastructure then build/load/validate/apply apps
mise run validate:k8s          # strict dry-run on ALREADY ready API/Namespaces/CRDs
mise run test:kind-validation  # native/custom positive, wrong-type and unknown-kind controls
mise run test:kind-baseline    # seeds completed history, diamond + API restart/keyed replay
mise run test:kind-persistence # replaces PG/Temporal Pods and safely reruns schemas
```

Run mutating tasks **sequentially and exclusively**, not while other executions
or experiments are active. `k8s:up` reruns bootstrap Jobs and restores the static,
fixed-replica application configuration; do not run it against an active scaling
experiment. A rerun preserves PostgreSQL history, not arbitrary experiment specs.

The orchestrator runs in `temporal-demo-orchestrator`; each worker runs in its
own `temporal-demo-worker-a` through `-d` namespace. Temporal's independent
namespace is `default`, endpoint `temporal.temporal.svc.cluster.local:7233`.
Baseline workers are fixed at one, with no discovery RBAC or app ScaledObjects.

In separate terminals, foreground tunnels bind localhost only:

```sh
mise run api:forward            # http://127.0.0.1:18080; Ctrl-C stops this tunnel
mise run ui:forward             # http://127.0.0.1:18088
# API_FORWARD_PORT=19080 mise run api:forward  # when occupied
curl -fsS -X POST -H 'Idempotency-Key: kind-demo' http://127.0.0.1:18080/runs
```

Tests allocate their own localhost tunnels and clean their exact child PIDs.
No manually running tunnel is required. Do not kill processes by name/pattern.

### Isolated integration phases, then combined acceptance

After baseline/persistence, run in this order:

```sh
mise run policies:install
mise run test:policies-integration
mise run apps:deploy-scaling
SCALING_SKIP_LONG=0 mise run test:scaling
mise run apps:deploy-discovery
mise run test:registry
mise run test:rbac
mise exec -- bash scripts/discovery.sh restore
```

Policy lifecycle uses temporary namespaces; scaling restores fixed workers;
discovery retains its static rollback snapshot until explicit restore (failures
also attempt restoration). Check cleanup errors before proceeding.
[Discovery](docs/discovery.md) explains eventual consistency, intended-set waits
and trust; [autoscaling](docs/autoscaling.md) explains native replica ownership
and why cooldown/running counts cannot guarantee uninterrupted activities.

Only after these phases pass, use the temporary combined smoke/recovery path.
Verify the node's Docker kind label and the known owned kube-system UID before
setting the safety variable; a current context alone is **not** a trust check.
On a newly created cluster record this identity from that owned creation.

```sh
mise exec -- docker inspect temporal-demo-control-plane \
  --format '{{index .Config.Labels "io.x-k8s.kind.cluster"}}'
KUBECONFIG="$PWD/.local/kubeconfig" mise exec -- kubectl \
  --context kind-temporal-demo get namespace kube-system -o jsonpath='{.metadata.uid}'
export TASK10_TRUSTED_UID='<verified owned kube-system UID>'
mise run smoke
mise run test:resilience
```

Both public tasks require a completed-history witness, fixed/static baseline,
Ready policies and no existing discovery RBAC/scalers or running executions.
They install a **temporary combined** overlay and restore the baseline, not a
persistent combined deployment. They preserve current cluster history, canonical
Services, PVC and immutable Secret identities. They record exact owned execution
intents before POST so ambiguous acceptance can be reconciled safely. Proof
paths printed as `.local/task10-*` describe the script lineage, not the date or
task that executed them. See [experiments and 14-criterion map](docs/experiments.md).

### Storage and destructive boundary

Compose named volumes and kind's local PVC preserve histories across ordinary
PostgreSQL/Temporal replacement. Matching server/admin-tools **1.32.0** initialize
and migrate separate default/visibility databases using `postgres12` before
server startup; namespace bootstrap completes before app pollers. Safe schema
reruns do not mean arbitrary downgrades or manual SQL changes are supported.

Kind reuses immutable `temporal/postgres-credentials` with `data-postgres-0`.
A retained PVC without its Secret fails closed instead of rotating the password.
Never delete that Secret to repair authentication. Local PVCs are not backups.

```sh
mise run k8s:down              # DESTRUCTIVE: ONLY temporal-demo, ALL local PVC/history
# Deliberate reset: down, then k8s:up; old histories must be absent.
```

Down is idempotent but not an ordinary test cleanup command. Verify exact named
ownership and no unrelated active executions before deliberate deletion.
Authorized local reset evidence and disposable CI resets are distinct from
ordinary preservation evidence. Tests normally leave a healthy retained stack.

## Validation, diagnostics and navigation

`validate:manifests` lints/renders the application chart, validates its **complete**
intended graph, and verifies/renders pinned controller archives. It cannot discover
unavailable CRD schemas offline. `validate:k8s` requires the ready owned API and
never bootstraps. Startup validates the graph before application Namespace
creation, then strict server-dry-runs and applies the **same render**. Real API
controls reject unknown kinds and wrong native/custom field types without
persisting fixtures. Helm renders charts; no Helm releases, local CRD catalog,
kubeconform or `k8s/apps` bundle is used. See [infrastructure](k8s/infrastructure/README.md).

- API not ready: inspect `/workers` diagnostics and Temporal readiness. Invalid
  graphs may be synchronized/ready for diagnostics yet reject new starts.
- Missing workers: inspect namespace labels, generated ConfigMaps, policy Ready
  conditions and each namespace's RoleBinding. Wait for intended names **and hash**;
  nonempty/valid alone can be a partially reconciled rollout.
- Stuck at zero: inspect ScaledObject Ready/Active and KEDA operator errors;
  cached Temporal pollers are not living Pods. Discovery is not capacity proof.
- `mise run kind:diagnostics`: scoped status/events/app logs and redacted schema
  failure evidence. `mise exec -- bash scripts/compose.sh diagnostics`: scoped
  Compose app logs/status. Do not dump environment, Secrets or raw SQL logs.
- On failure preserve printed proof directories and restoration errors. Do not
  reset storage, widen permissions or increase timeouts to hide a failed test.

[Architecture](docs/architecture.md) covers scheduling, snapshots and safety;
[API](docs/api.md) covers exact HTTP schemas; [versions](docs/versions.md) separates
pins from executed platform evidence; [experiments](docs/experiments.md) maps all
acceptance criteria, current results, CI coverage and known limits.
