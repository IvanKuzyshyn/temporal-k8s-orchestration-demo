# Temporal Kubernetes orchestration demo

One local demo: Kyverno discovers worker registrations, Temporal schedules a
dependency graph, and KEDA activates worker Pods from zero when tasks arrive.

```text
           worker-b
          /        \
worker-a             worker-d
          \        /
           worker-c
```

Each child runs a five-second simulated activity. Workflow inputs/results carry
execution identities, not business payloads. Workers are idle at zero replicas;
the orchestrator remains running. This localhost demo is not a production service.

## Prerequisites

- Docker Engine/Desktop running with Linux containers.
- [mise](https://mise.jdx.dev/), Git, Bash, curl, jq and OpenSSL.
- For local race tests: a C compiler (Xcode command-line tools on macOS or GCC on Linux).
- Network access for the first tool, image, module and pinned chart downloads.
- Approximately 4–6 available CPUs and 8–12 GB Docker memory is a starting estimate,
  not a measured minimum or a startup guarantee.

`mise install` installs the repository's pinned Go, kind, kubectl, Helm and Kyverno
CLI tools. Use `mise run`/`mise exec`, not arbitrary global tool versions. See
[versions](docs/versions.md) for pins and verification scope.

## Start

From the project root:

```sh
mise install
mise run up
```

`up` creates the dedicated `temporal-demo` kind cluster, configures PostgreSQL,
Temporal/UI, KEDA and Kyverno, builds/loads the app images and installs the full
live-discovery/scale-to-zero deployment. No separate deployment phases are needed.

Once infrastructure and the API are ready, `up` polls **`GET /graph` every second
for up to 120 seconds**. It waits for the complete expected workers and their
dependencies, not merely a nonempty valid subset. Requests are bounded and do not
overlap. On timeout it reports the last HTTP outcome, validation problems and
missing/extra/mismatched workers. Image builds and earlier bootstrap readiness
are outside this two-minute graph budget.

On success:

```text
Temporal UI: http://127.0.0.1:18088
API:         http://127.0.0.1:18080
```

The localhost tunnels run in the background. Open the UI and select Temporal
namespace **default**.

## Run a workflow

```sh
curl -fsS -X POST http://127.0.0.1:18080/runs
```

Send **zero body bytes**, not `{}`. The response contains `workflowId`, `runId`,
`planHash` and status. A runs first, B/C then execute concurrently, and D waits for
both. KEDA polls every five seconds; activation includes Pod startup time. Workers
return to zero after idle cooldown (120 seconds), not immediately on completion.

For retries, choose a key once and reuse it:

```sh
curl -fsS -X POST -H 'Idempotency-Key: my-first-run' http://127.0.0.1:18080/runs

workflow_id='paste-workflowId-here'
curl -fsS "http://127.0.0.1:18080/runs/$workflow_id"
curl -fsS http://127.0.0.1:18080/graph
```

Same key resolves the original execution, including after restart or graph
changes. A new key or unkeyed POST starts new work. A lost response/503 may occur
**after** acceptance; retry the same key rather than blindly starting more work.
Deduplication lasts only while Temporal retains history (configured retention:
72 hours). See [HTTP API](docs/api.md).

Each new run validates its own current discovery snapshot. Startup does not
freeze the execution graph; existing keyed executions resolve independently of
current graph validity.

## Stop, resume and delete

```sh
mise run down       # stop tunnels and node; retain cluster, credentials and history
mise run up         # resume the retained installation and restore tunnels
mise run destroy    # DESTRUCTIVE: delete this demo cluster and all local history
```

`down` leaves Docker and unrelated containers/clusters running. It interrupts
active work; it is not a graceful workflow pause, and deadlines can expire while
the cluster is stopped. Docker must be running to perform lifecycle operations.

Repeated `up` resumes installed workloads **without rebuilding images or rerunning
schema Jobs**. It must not take replica ownership back from KEDA. To install source
changes, deliberately recreate with `destroy` then `up`; this loses local history.

### Upgrading an older checkout

An existing installation from before `/graph` still has old app images and may
lack the generated registration expectation. Updating Git alone does not upgrade
it. From the checkout that owns it, explicitly run `destroy` then `up` when you
are ready to discard its history. Legacy clusters without a lifecycle ownership
record are refused, never silently adopted; verify ownership and deliberately
remove such a legacy cluster before creating a new one.

## Kubernetes monitoring

Each worker has its own namespace:

- `worker-a`: `temporal-demo-worker-a`
- `worker-b`: `temporal-demo-worker-b`
- `worker-c`: `temporal-demo-worker-c`
- `worker-d`: `temporal-demo-worker-d`

The orchestrator is in `temporal-demo-orchestrator`; Temporal/PostgreSQL/UI are in
`temporal`; controllers are in `keda` and `kyverno`. Kubernetes namespaces are
separate from Temporal namespace `default`.

```sh
k9s --kubeconfig "$PWD/.local/kubeconfig" --context kind-temporal-demo --all-namespaces

mise exec -- kubectl --kubeconfig "$PWD/.local/kubeconfig" \
  --context kind-temporal-demo get pods -A
mise exec -- kubectl --kubeconfig "$PWD/.local/kubeconfig" \
  --context kind-temporal-demo get scaledobjects -A
```

Zero worker Pods is normal when idle. In k9s use `:deploy`, `:scaledobjects`,
`:cm`, `:events` and `l` for logs. The Temporal UI shows workflow histories,
child execution ordering, retries and results.

Kyverno generates **registration ConfigMaps** from annotated Services; the chart
creates the Deployments and ScaledObjects. KEDA scales those existing Deployments.
Registration is not evidence that a Pod is currently running.

## Ownership and troubleshooting

- Run lifecycle commands from the same checkout/worktree; identity, kubeconfig
  and tunnel state are retained in ignored `.local/`. A foreign `KUBECONFIG` is
  refused; unset it rather than silently switching contexts.
- Only one named `temporal-demo` cluster exists per Docker daemon. Foreign or
  replaced node identities are refused. Do not delete PostgreSQL credentials to
  repair authentication; credentials and retained storage must stay paired.
- API/UI ports occupied: `API_FORWARD_PORT=19080 UI_FORWARD_PORT=19088 mise run up`.
  Another process is never killed to free a port.
- Logs: `.local/lifecycle/api.log` and `ui.log`. Safe failure diagnostics:
  `mise exec -- bash scripts/kind-diagnostics.sh`. Do not dump Secrets or raw SQL logs.
- A stale `.local/lifecycle.lock` after an uncatchable interruption requires
  confirming no lifecycle command is running before removing that lock directory.
- Incomplete app bootstrap can be retried with `up`. Failed kind creation does not
  establish ownership and can require explicit operator cleanup.
- Local PVCs preserve restart history; they are not backups. `destroy` leaves
  shared image/chart caches and unrelated Docker resources intact.

## Development checks

```sh
mise run validate          # formatting, race suite, vet, policies, shell/chart checks
mise run test              # race-enabled Go tests
mise run test:integration  # creates/destroys a NEW demo; refuses an existing cluster
```

The real lifecycle test is intended for disposable CI or an unused Docker
environment, not your retained demo. [Test ownership](docs/testing.md) explains
which layer covers idempotency, discovery, graph validation and lifecycle safety.

[Architecture](docs/architecture.md) · [API](docs/api.md) ·
[Discovery](docs/discovery.md) · [Autoscaling](docs/autoscaling.md) ·
[Infrastructure](k8s/infrastructure/README.md)
