# Live discovery

Live Kubernetes registration is unconditional. There is no static file registry,
Compose discovery mode, staged discovery command or `/workers` endpoint.

## Source contract

The worker chart renders one Namespace and annotated Service per worker. Namespaces
have `temporal-demo.io/worker-namespace: "true"`. Selected Services declare:

- `temporal-worker: "true"`
- `temporal-worker-run-after`: comma-separated worker names; roots use an empty value.

The policy derives task queue `<worker-name>.v1.temporal` and fixes workflow type
`ExampleChildWorkflow`; these are not separate Service annotations.

The pinned Kyverno policy validates registration fields, then generates a
same-namespace `temporal-worker-<service>` ConfigMap containing workerName,
taskQueue, workflowType, runAfter, sourceServiceName, sourceServiceUID,
sourceServiceNamespace and schemaVersion. ConfigMaps have
`temporal-demo.io/registration: "true"`.
Generation synchronizes source changes and generated resources carry ownership
metadata. Do not manually edit generated registrations.

Kyverno does not generate Deployments or ScaledObjects; Helm creates those. Worker
Pods disable service-account token automount. Orchestrator RBAC lists/watches
Namespaces cluster-wide and gets/lists/watches ConfigMaps only in worker namespaces.
It cannot read Secrets or register workers by writing Kubernetes resources.

## Snapshot safety

The provider follows selected Namespace membership and per-namespace ConfigMap
informer views. It does not expose a new graph as synchronized until the relevant
views are synchronized. Reconnect/relist, permission failures and in-flight list
attempts invalidate synchronization rather than reusing a stale-ready flag.
Snapshots own their data; callers cannot mutate shared registration state.

`registry.ValidateSnapshot` is shared by the read-only graph endpoint and new-run
admission. It rejects unsynchronized/empty/invalid graphs and checks copied nodes
against their rebuilt canonical graph. Validation problems block new runs, not
health diagnostics or replay of an existing durable key.

## Complete startup readiness

Bootstrap projects intended registrations from the actual rendered Namespaces and
Services into sorted expected nodes, normalizing dependency whitespace/duplicates
consistently with Go graph validation. It atomically writes
`.local/lifecycle/expected-workers.json` before marking installation initialized.
That generated state is retained during down/up; missing/malformed state fails
closed. There is no separately maintained expected-graph JSON in source control.

After controllers, policy, schema, app and API readiness, `up` polls `/graph`
immediately and then at one-second intervals for up to 120 elapsed seconds. It
requires a synchronized valid graph and exact expected node identities, queues,
workflow types and dependencies. Requests are sequential and capped at the lesser
of two seconds or the remaining budget. Sleeps are capped too. Timeout prints
last HTTP/connection outcome, graph problems and missing/extra/mismatched workers.

This file is lifecycle bookkeeping, not mounted into the orchestrator, not a
fallback registry and never an execution input. It does not replace fresh POST
snapshot validation or freeze discovery after startup.

Use `curl -sS http://127.0.0.1:18080/graph | jq` to inspect diagnostics. See the
[API contract](api.md) for 200/422/503 semantics and [test ownership](testing.md)
for independent informer race cases.
