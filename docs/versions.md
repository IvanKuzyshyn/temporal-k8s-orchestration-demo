# Pins and verification scope

Pins are reproducible inputs, not a compatibility guarantee for every platform.
Deployment files are authoritative; upgrades require separate validation.

## Authoritative inputs

- `mise.toml`: mise minimum 2026.10.17; Go 1.27.3, kind 0.33.0,
  kubectl 1.35.8, Helm 4.3.1, Kyverno CLI 1.19.2.
- `go.mod`/`go.sum`: Temporal Go SDK 1.50.0, API 1.63.5, chi 5.5.0,
  Gomega 1.38.2, protobuf 1.36.11 and YAML 3.0.4. Tests use standard testing.T.
- `k8s/infrastructure/versions.env`: Kubernetes 1.35.8, KEDA chart/app 2.21.0,
  Kyverno chart 3.9.2/app 1.19.2, node image digest and chart archive checksums.
- `k8s/infrastructure/temporal.yaml`: PostgreSQL 16.15, Temporal server/admin-tools
  1.32.0, independently released UI 2.48.0, all image-digest pinned. Matching
  PostgreSQL schema tools initialize default and visibility databases. No SQLite,
  auto-setup image or `temporal server start-dev` is used.
- `deploy/*.Dockerfile`: digest-pinned Go Bookworm builder and Debian slim runtime.
- `k8s/infrastructure/{keda,kyverno}-values.yaml`: controller image digests.

KEDA's [2.21 support matrix](https://keda.sh/docs/2.21/operate/cluster/) includes
Kubernetes 1.35. The [Temporal scaler](https://keda.sh/docs/2.21/scalers/temporal/)
supports both workflow and activity backlog; this demo fixes running-workflow
count off. Supported API versions and available multi-architecture images alone
do not prove runtime acceptance. Kyverno ClusterPolicy deprecation warnings remain
visible rather than hidden.

Chart checksums are verified even on cache hits. Exact archives:
[KEDA](https://kedacore.github.io/charts/keda-2.21.0.tgz) and
[Kyverno](https://kyverno.github.io/kyverno/kyverno-3.9.2.tgz).
Controller artifacts are rendered and kubectl-managed, not Helm releases.

Clean app builds use the Git SHA; dirty local builds get unique tags, never
`latest`. kind directly builds/loads these images. Repeated `up` resumes installed
images rather than silently upgrading them. Docker contexts exclude local
credentials and lifecycle state.

## Evidence and limitations

The preceding lifecycle implementation was exercised locally on macOS ARM64 with
Linux ARM64 Docker containers. Minimal-demo changes have race-enabled Go/chart/
policy validation and app image builds on that host. Its existing developer demo
was deliberately retained: the new disposable runner correctly refused to reuse
or delete it, so this is **not** a fresh minimal-demo live ARM64 acceptance claim.

The new full lifecycle scenario passed on disposable Ubuntu 24.04 Linux CI at
step 4, commit `fa2e96e`:
[Kind integration 43100020101](https://github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/actions/runs/43100020101).
This establishes that revision's live scenario, not later revisions or universal
platform support. Use each stacked PR's current checks for exact-head evidence.
No Windows, alternate Kubernetes release or production readiness is claimed.

## CI contract

Two workflows remain: one fast Validate job and one disposable Kind integration
job. Only Validate runs the complete fast suite. Kind invokes the public lifecycle
runner, which refuses preexisting resources, checks real workflow histories and
scaling, repeats stop/resume, tests durable replay and destroys its owned demo.

Workflows run on pushes to main (including merged PRs), pull_request and
workflow_dispatch, never pull_request_target. Token permission is contents:read. External checkout and mise actions are SHA-pinned;
checkout credential persistence is disabled. No write token, OIDC or deployment
permission is requested. Kubeconfigs, Secret data and credential files are not
uploaded as artifacts. Console diagnostics are scoped and avoid raw SQL Job logs.

Configured traps/always cleanup cannot guarantee cleanup after host loss or force
cancellation. CI uses disposable runners; local testing must not sacrifice a
retained user cluster. [Test ownership](testing.md) describes the retained checks.
