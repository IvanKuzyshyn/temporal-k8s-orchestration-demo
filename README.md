# Temporal Kubernetes orchestration demo

A local demo of **dependency-based workflows with workers that scale to zero**.
Temporal coordinates a four-worker diamond; Kubernetes starts worker Pods when
work arrives and scales them back to zero when idle.

```text
           worker-b
          /        \
worker-a             worker-d
          \        /
           worker-c
```

## How it works

- **kind** runs the local Kubernetes cluster in Docker.
- **Orchestrator** exposes the HTTP API and starts a Temporal parent workflow.
- **Kyverno** discovers annotated worker Services and generates registration
  ConfigMaps. The orchestrator reads these to build the dependency graph.
- **Temporal + PostgreSQL** schedule workflows and persist execution history.
- **KEDA** watches Temporal queue backlog and scales each worker Deployment 0–1.
- **Workers** execute five-second demo activities: A first, B/C concurrently, then D.

The chart creates Deployments and ScaledObjects; Kyverno registers the workers,
and KEDA scales them. The orchestrator stays running while workers are idle at
zero. Worker namespaces are `temporal-demo-worker-a` through
`temporal-demo-worker-d`.

## Start and stop

Prerequisites: running Docker, [mise](https://mise.jdx.dev/), Bash, curl, jq and
OpenSSL. Run these commands from the repository root:

```sh
mise install
mise run up
```

When ready, open **Temporal UI: http://127.0.0.1:18088** (namespace `default`).
The API is at **http://127.0.0.1:18080**. Startup waits for the complete worker graph;
detailed logs are saved in `.local/lifecycle/up.log` and printed on failure.

```sh
mise run down       # stop; keep data and workflow history
mise run up         # resume
mise run destroy    # DELETE the cluster, data and workflow history
```

Repeated `up` resumes installed images; it does not upgrade them. To deploy source
changes or upgrade an older installation, explicitly `destroy` then `up`—**this
loses local history**. Lifecycle commands refuse clusters owned by another checkout.

This also applies to the **Kyverno CEL policy migration**: older demos installed
`ClusterPolicy` resources, while current source uses `ValidatingPolicy` and
`GeneratingPolicy`. An initialized cluster skips bootstrap on repeated `up`, so
it will not migrate those policies. Run `mise run destroy` from the owning
checkout, then `mise run up` from the checkout you want to use. Do not run both
old and new generation policies against the same registrations.

## Run a workflow and inspect the graph

```sh
curl -fsS -X POST http://127.0.0.1:18080/runs
curl -fsS http://127.0.0.1:18080/graph | jq
```

The POST must have **no body** (not even `{}`). View execution in Temporal UI.
Workers can take a few seconds to activate and return to zero after the
120-second idle cooldown. For retry-safe requests, see [idempotency and run status](docs/api.md).

## Use kubectl and k9s

Configure only the current terminal to use this checkout's local kubeconfig:

```sh
export KUBECONFIG="$PWD/.local/kubeconfig"
mise exec -- kubectl config use-context kind-temporal-demo
mise exec -- kubectl get pods -A
mise exec -- kubectl get scaledobjects -A
```

With [k9s](https://k9scli.io/) installed separately:

```sh
k9s --context kind-temporal-demo --all-namespaces
```

Zero worker Pods when idle is expected. To stop using the demo kubeconfig in this
terminal, run `unset KUBECONFIG`. This does not change your default kubeconfig.

## More details

[Architecture](docs/architecture.md) · [HTTP API](docs/api.md) ·
[Discovery](docs/discovery.md) · [Autoscaling](docs/autoscaling.md) ·
[Development/tests](docs/testing.md) · [Versions](docs/versions.md)
