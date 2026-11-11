# Pins, platforms and evidence provenance

Pins are deployment inputs, not a compatibility claim for every architecture.
Do not upgrade them to fix an acceptance failure without separate review.

## Authoritative selections

- `mise.toml`: minimum mise **2026.10.17**; Go **1.27.3** (module language
  1.27.0), kind **0.33.0**, kubectl **1.35.8**, Helm **4.3.1**, Kyverno CLI
  **1.19.2**. Race tests require a host C compiler.
- `go.mod`/`go.sum`: Temporal Go SDK **1.50.0**, Temporal API **1.63.5**,
  chi **5.5.0**, Gomega **1.38.2**, protobuf **1.36.11** and
  `go.yaml.in/yaml/v3` **3.0.4**. Tests use standard testing.T and scoped
  Gomega, not a Ginkgo runner. Runtime code is not standard-library-only.
- `k8s/infrastructure/versions.env`: Kubernetes **1.35.8**, KEDA chart/app
  **2.21.0**, Kyverno chart **3.9.2** / app **1.19.2**. Controller image
  release/digest pins are in `keda-values.yaml` and `kyverno-values.yaml`.
- `deploy/compose.yaml`, `deploy/*.Dockerfile` and
  `k8s/infrastructure/temporal.yaml`: PostgreSQL **16.15**, Temporal
  server/admin-tools **1.32.0**, independently released UI **2.48.0**.
  Matching `postgres12` tools initialize/migrate both databases: observed stored
  schema versions are default **1.19**, visibility **1.14**. No SQLite,
  auto-setup image or `temporal server start-dev` is used.

SDK 1.50.0 requires Go 1.26; chi 5.5.0 requires Go 1.24; Gomega 1.38.2
requires Go 1.23; YAML 3.0.4 requires Go 1.16. The selected Go satisfies them.
Sources: [SDK tag](https://github.com/temporalio/sdk-go/tree/v1.50.0),
[chi tag](https://github.com/go-chi/chi/tree/v5.5.0),
[Gomega tag](https://github.com/onsi/gomega/tree/v1.38.2),
[YAML tag](https://github.com/yaml/go-yaml/tree/v3.0.4).

KEDA's [2.21 support matrix](https://keda.sh/docs/2.21/operate/cluster/)
lists Kubernetes 1.34–1.36, including 1.35.8. Its
[Temporal scaler](https://keda.sh/docs/2.21/scalers/temporal/) supports both
queue types and optional running-workflow counts. Kyverno's chart minimum
Kubernetes version is >=1.25.0-0; that minimum alone is not tested-platform proof.
The pinned ClusterPolicy API is supported but deprecated; warnings are retained.
See [native lifecycle evidence](discovery.md), not just controller readiness.

## Immutable image and chart inputs

These multi-platform index digests include linux/arm64 and linux/amd64;
manifest availability alone does not mean runtime acceptance on both platforms.
Deployment files remain authoritative if comparing against this list.

- Go build: `golang:1.27.3-bookworm@sha256:972b4110ba0f47539a975c31e52c64765e1ff19f2d74469ae4a595c6676e80a5`.
- App runtime: `debian:bookworm-slim@sha256:7ff6ba31527b9e336162fa061746c366c4b2032f4d2cff2078bea43f78efe3f8`.
- PostgreSQL: `postgres:16.15@sha256:9e2cf6287f25c3151f4c9421ccce2b17f3e60061c111a29165e0d2cdd9ff0a97`.
- Server: `temporalio/server:1.32.0@sha256:c3e752127759616bb1615e0f9ba0e21635aeb5fdeb922de4f371c350955f46ae`.
- Admin/schema: `temporalio/admin-tools:1.32.0@sha256:a9f84fb9a374b2374fe2e67c8efc0468ff3f1c66c8a0b14597ec86e349e62bca`.
- UI: `temporalio/ui:2.48.0@sha256:5bc8712de27029b64a9cad156221b082a43ae27bfb108fc1743076eaabf1437f`.
- kind node: `kindest/node:v1.35.8@sha256:07b2536e30b803ed61d1677a79df6115f798ce64c80f9e22f6ed45afd09323c0`.

Chart archive SHA256 (checked even on cache hits): KEDA
`a7da56bf41a33dad9d2fc82a6fdecee552fff947d80bf1e1dbcf4db0f9e26dee`;
Kyverno `f6fe7184544d5e2ee33a77808c09e67aa603ce6560cc197038fde31d727c861e`.
Sources: [KEDA index](https://kedacore.github.io/charts/index.yaml),
[Kyverno index](https://kyverno.github.io/kyverno/index.yaml),
[kind release](https://github.com/kubernetes-sigs/kind/releases/tag/v0.33.0),
[Temporal schema source](https://github.com/temporalio/temporal/tree/v1.32.0/schema/postgresql/v12).
Charts are runtime-rendered with Helm and installed with kubectl, **not Helm
releases**. There is no active kubeconform/schema exporter/local CRD catalog.
Offline checks are Helm/values-schema/full graph; strict native/custom-resource
validation uses the actual ready Kubernetes API. Earlier plan/catalog references
and old provisioning wrappers were superseded by this approved native layout.

Clean app builds use the Git SHA; dirty local builds get unique tags, never
`latest`. Compose records `.local/compose-image-tag`; kind builds directly and
loads its own tags, without invoking Compose. Docker build contexts exclude
credentials/local proof. The resilience relay has a test-specific Docker context
allowlist and uses existing Go/Debian pins, not a production image/test switch.

<a id="approved-staged-gate-and-evidence-map"></a>

## Executed platforms — keep the gates separate

Current Task11 local execution: Darwin **arm64**, Docker Desktop daemon
**29.5.3 linux/arm64**, Compose **5.1.4**; pinned tools above were observed in
`.local/task11/versions.log`. The daemon is Linux even though the Go host tests
run on Darwin. Exact current commands/exits/timings and limitations are recorded
in [experiments](experiments.md) and the ignored Task11 report/proof. No Darwin
amd64, Windows, other Kubernetes versions or production platform is certified.

Historical Linux **amd64**, Ubuntu **24.04**, Task10 exact head
`ee0448a88711a97bcd675ee0a6a8174adba0f945`, attempt **1**:

- [Validate run 43008847772](https://github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/actions/runs/43008847772): success, **5m44s**.
- [Compose run 43008848196](https://github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/actions/runs/43008848196): success, **11m00s**.
- [Kind run 43008847672](https://github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/actions/runs/43008847672): success, **32m06s**.

Controller archived exact-head/attempt/step JSON and raw logs as
`.superpowers/sdd/2026-10-17-temporal-k8s-orchestration/task10-ci-{validate,compose,kind}.{json,log}`.
Those were inspected during Task11. Kind step records include policy lifecycle,
scaling, registry/RBAC, combined smoke, destructive down/down/up, old-history 404,
fresh baseline/smoke, artifacts and cleanup. **Longer snapshot/recovery was
skipped on that PR run**, not passed. Prior successful jobs are not current
Task11 CI or manual resilience evidence. Exact Task11-head cumulative Linux CI
and independent reviews remain controller-owned pending gates.

## CI inputs and ownership audit

All workflows use `pull_request`/`workflow_dispatch`, never pull_request_target;
permissions are only `contents: read`. External actions are SHA-pinned:
checkout v7.0.1 `3d3c42e5aac5ba805825da76410c181273ba90b1` (credential persistence
and global safe-directory changes disabled); setup-mise uses mise-action v5.2.2
`8418fedb1110381a0d3ba51a7a415d7c1b884550`, exact mise version, cache disabled;
upload-artifact v4.6.2 `ea165f8d65b6e75b540449e92b4886f43607fa02`.
No write token, OIDC or deployment permission is requested.

Validate/Compose/Kind job limits are **10/25/45 minutes**, unchanged. All run
`mise run validate`; Compose explicitly builds both images before up/smoke and
isolated persistence/reset. Kind retains two ups, real API controls,
baseline/persistence, policy lifecycle, scaling, registry/RBAC, smoke before/after
reset, 404 and fresh baseline. Manual dispatch adds long in-flight scaling and
the five-case resilience selection. Go test build tags/selectors are described
in [experiments](experiments.md); untagged race tests do not replace live suites.

Compose always attempts exact CI-project/child-fixture cleanup and aggregates
errors. Kind failure diagnostics are scoped/secret-redacted; combined proof is
uploaded even on failure, followed by always-attempted deletion of only the
runner's temporal-demo cluster. Artifact paths include `.local/task10-smoke-*`
and `.local/task10-resilience-*`, not kubeconfig, env files or Secret data.
Validate needs no runtime cleanup. Job cancellation/platform loss can prevent
any trap/always step; do not describe configured cleanup as an absolute guarantee.
