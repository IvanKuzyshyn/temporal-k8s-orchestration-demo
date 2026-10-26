# Version selections and evidence

Research observed on 2026-10-22 (UTC host clock); the 2026-10-17 design/plan date is not the research date. Task 1 selects versions before dependency installation. Go/mise were bootstrapped in Task 1; Task 2 installs the Go SDK described below. At that research stage no cluster, server, chart or database was installed. Task 4 subsequently runs the Compose subset below; this is **not a runtime-tested Kubernetes/controller matrix**. Upstream sources were retrieved during this assignment; mutable documentation is not evidence that this repository ran those systems.

## Foundation tools

- Go **1.27.3**, from the stable release feed <https://go.dev/dl/?mode=json>. Exact pin in `mise.toml`; module language level 1.27.0. Host: **Darwin arm64**. Race testing requires a working C toolchain.
- mise **2026.10.17**, <https://github.com/jdx/mise/releases/tag/v2026.10.17>. Project-local bootstrap binary checked against upstream SHASUMS256.txt; no global install or user configuration changes. CI pins the same version and the config requires it as a minimum.
- The original Task 1 foundation was standard-library-only, so its initial `go mod tidy` legitimately produced no `go.sum`. The follow-up test-only migration now pins **Gomega v1.38.2** in `go.mod` and records its dependency checksums in `go.sum`; runtime product code remains standard-library-only. Release: <https://github.com/onsi/gomega/releases/tag/v1.38.2>; [tagged go.mod](https://github.com/onsi/gomega/blob/v1.38.2/go.mod) requires Go **1.23.0**, satisfied by pinned Go 1.27.3. The Go module proxy release metadata identifies tag commit `79b8a75a10e36ee598344c040317f5ab1c907f53` (2025-08-26). Tests retain standard `testing.T` with per-test/subtest `gomega.NewWithT(t)`; no Ginkgo runner or project requirement is added. Tidy includes Ginkgo checksums because Gomega's own tests depend on it. Local baseline/focused/uncached validation evidence is recorded in `.superpowers/sdd/2026-10-17-temporal-k8s-orchestration/gomega-report.md`; this does not close any Linux CI or infrastructure gate below.

## Approved staged gate and evidence map

Controller ruling for review I1: preserve the Go-only Task 1 foundation; runtime compatibility acceptance belongs to each consuming task below. Research selects candidate pins, not a tested matrix. Each task must retain exact commands, versions, architecture and results for both the local host and Linux CI; documentation, configured checks or skipped runs do not close an evidence gate. Existing task tests and cumulative CI remain required.

- **Task 1 — Go/mise foundation:** verify pinned Go/mise and formatting, full-package race tests and vet on the host and Linux CI using `mise run validate`. Darwin arm64 evidence is recorded in the Task 1 report. **Task 1 runtime acceptance remains pending Linux CI until the controller observes and records its passing result.**
- **Task 2 — SDK contracts:** Temporal Go SDK **v1.50.0** is pinned. Darwin arm64 workflow/activity tests, targeted race tests, and uncached full validation pass (commands and results in `.superpowers/sdd/2026-10-17-temporal-k8s-orchestration/task-2-report.md`). Scheduling, failure, SDK child cancellation, and heartbeating activity-context cancellation are exercised without a live server. **Linux CI acceptance remains pending controller observation.** These tests do not prove server/image/scaler compatibility or the future API's parent start policy.
- **Task 4 — Compose runtime matrix:** verify selected image availability/digests and architecture support, exact SDK/server/PostgreSQL compatibility, matching schema-tool packaging, default and visibility schema setup/migrations before readiness, and history persistence across container replacement with named volumes. Run the Compose checks on the host and Linux CI; record reset behavior separately from persistence. Darwin-host/Linux ARM64 Docker evidence is recorded below; Linux amd64 CI remains pending controller observation.
- **Task 6 — kind infrastructure matrix:** verify selected kind/Kubernetes, Helm charts and controller/image compatibility on the host and Linux CI. Check CRD schemas and controller/webhook readiness, repeatable database schema Jobs before Temporal readiness, and history persistence across Pod replacement with the PVC; verify and document destructive cluster/PVC reset behavior. Evidence: pending.
- **Task 7 — no-poller scaler behavior:** on the host and Linux CI, prove that the selected Temporal/KEDA pair exposes workflow and activity queue backlog with no active pollers and activates workers from zero using `queueTypes: "workflow,activity"`; retain scale-down and interruption evidence required by the task. Optional running-workflow counts must not be treated as strict in-flight protection. Evidence: pending.
- **Task 8 — admission and cleanup before Task 9:** on the host and Linux CI, verify source-Service CREATE/UPDATE admission (malformed dependencies/self-reference rejected; valid roots and registration disable/removal allowed) and real-cluster registration create/update/delete/disable/annotation-removal/preexisting-resource lifecycle against the pinned Kyverno release. Source deletion and loss of policy match require separate assertions. If native generation is insufficient, choose and test explicit cleanup/source validation. **Task 9 Kubernetes discovery must not be enabled until this gate passes.** Evidence: pending.

These assignments stage the blocking gate; they do not waive host/Linux verification, any existing tests, or cleanup-before-discovery. A failed compatibility check that changes a design contract still requires stopping and updating the approved design.

## Consumed SDK selection and host evidence

Task 2 release metadata checked on 2026-10-25 (UTC host clock), before dependency installation:

- `curl -fsSL https://proxy.golang.org/go.temporal.io/sdk/@v/v1.50.0.info` returned v1.50.0, release timestamp `2026-10-15T21:02:36Z`, tag commit `e43cbba8d80f064ca93e4a8ebd9cc2e6dd15d024`.
- The matching `https://proxy.golang.org/go.temporal.io/sdk/@v/v1.50.0.mod` requires Go **1.26.0**; `mise exec -- go version` returned **go1.27.3 darwin/arm64**. `mise exec -- go get go.temporal.io/sdk@v1.50.0` and `mise exec -- go mod tidy` succeeded; `go.mod`/`go.sum` pin the SDK and transitive dependencies. Gomega remains **v1.38.2**, with standard `testing.T` and scoped assertions.
- Host verification uses the SDK's in-process test suite, not a Temporal server. Real-server cancellation delivery, parent execution timeout/retry start options, exact SDK/server compatibility, and infrastructure runtime gates remain separate consuming-task obligations. No Linux run is claimed here.
- Upstream release: <https://github.com/temporalio/sdk-go/releases/tag/v1.50.0>; [tagged go.mod](https://github.com/temporalio/sdk-go/blob/v1.50.0/go.mod).

## Consumed HTTP router selection and host evidence

Task 3 checked release metadata on **2026-10-25 UTC** before installation:

- `curl -fsSL https://proxy.golang.org/github.com/go-chi/chi/v5/@latest` returned **v5.5.0**, timestamp `2026-10-14T13:20:04Z`, tag commit `3a33ba578e0dd93444e0ff386cc26e665d92e31e`.
- `curl -fsSL https://proxy.golang.org/github.com/go-chi/chi/v5/@v/v5.5.0.mod` and `curl -fsSL https://raw.githubusercontent.com/go-chi/chi/v5.5.0/go.mod` both returned module `github.com/go-chi/chi/v5`, requiring **Go 1.24**. `mise exec -- go version` returned **go1.27.3 darwin/arm64**, satisfying that requirement. Release/tag: <https://github.com/go-chi/chi/releases/tag/v5.5.0>; [tagged module](https://github.com/go-chi/chi/blob/v5.5.0/go.mod).
- `mise exec -- go get github.com/go-chi/chi/v5@v5.5.0` and `mise exec -- go mod tidy` succeeded; `go.mod` and `go.sum` pin that released dependency. Temporal SDK **v1.50.0** and scoped Gomega **v1.38.2** remain unchanged.
- Host HTTP/service/SDK-adapter tests and orchestrator race tests pass without a live Temporal server. The adapter tests assert actual outgoing SDK options, including parent queue/type, 30-minute execution timeout, nil retry policy, reject-duplicate ID reuse, conflict-fail policy, immutable input, and (at the initial Task 3 revision) distinct request/plan Memo hashes. The approved payload-removal follow-up replaced this with **planHash-only Memo** and key-only duplicate identity; current code/tests have no request hash or business payload. Durable lookup/Memo decoding, completed-run retrieval, unfinished nonblocking retrieval and error mapping are exercised through narrow SDK-client fakes, not a giant `client.Client` fake.
- Exact RED/GREEN, focused/race and full uncached `mise run validate` evidence is recorded in `.superpowers/sdd/2026-10-17-temporal-k8s-orchestration/task-3-report.md`. Existing cumulative CI discovers the new packages automatically; **Linux CI acceptance remains pending controller observation**. No live server, cluster, persistence experiment or infrastructure gate is claimed by these unit tests.

## Infrastructure selections (Compose consumed in Task 4; later tasks unverified)

- Temporal server **1.32.0**, schema/admin tooling **1.32.0** from the same release, chart **1.7.0** (its appVersion is 1.32.0). Release: <https://github.com/temporalio/temporal/releases/tag/v1.32.0>; published chart index: <https://go.temporal.io/helm-charts/index.yaml>. Schema source: <https://github.com/temporalio/temporal/tree/v1.32.0/schema/postgresql/v12>. Use `postgres12` for default and visibility stores, separate databases `temporal` and `temporal_visibility`; schema setup/migration must precede server startup. Pin image digests and verify schema-tool packaging in the consuming infrastructure task.
- PostgreSQL **16.15**: listed as a supported minor in <https://www.postgresql.org/support/versioning/>. Temporal [visibility requirements](https://docs.temporal.io/self-hosted-guide/visibility) document PostgreSQL 12+ for server 1.20+; [PostgreSQL configuration](https://docs.temporal.io/self-hosted-guide/visibility/postgresql) describes postgres12 configuration. These were selection-time documentation, not experiments. Task 4 Compose volume/restart/reset evidence is below; kind PVC preservation and cluster reset remain untested.
- kind **0.33.0**, Kubernetes **1.35.8**, image `kindest/node:v1.35.8@sha256:07b2536e30b803ed61d1677a79df6115f798ce64c80f9e22f6ed45afd09323c0`, listed in <https://github.com/kubernetes-sigs/kind/releases/tag/v0.33.0>. Do not use kind's newer default node version blindly.
- Helm **4.3.1**, <https://github.com/helm/helm/releases/tag/v4.3.1>. Rendering/install compatibility with these charts is still to be exercised.
- KEDA app **2.21.0**, chart **2.21.0**: <https://github.com/kedacore/keda/releases/tag/v2.21.0>, <https://kedacore.github.io/charts/index.yaml>. The [2.21 matrix](https://keda.sh/docs/2.21/operate/cluster/) lists Kubernetes 1.34–1.36, including selected 1.35. [Pinned scaler docs](https://keda.sh/docs/2.21/scalers/temporal/) support `queueTypes: "workflow,activity"`, activation threshold and optional `includeRunningWorkflowCount`. Running counts are visibility-based and not strict in-flight protection. [Release source](https://github.com/kedacore/keda/blob/v2.21.0/pkg/scalers/temporal_scaler.go) uses Temporal queue APIs; reliable backlog with zero pollers and this exact server pair **must still be tested** before discovery/scaling is combined.
- Kyverno app **1.19.2**, chart **3.9.2**: <https://github.com/kyverno/kyverno/releases/tag/v1.19.2>, <https://github.com/kyverno/kyverno/blob/v1.19.2/charts/kyverno/Chart.yaml>, <https://kyverno.github.io/kyverno/index.yaml>. Chart Kubernetes constraint is >=1.25.0-0 (a declared minimum, not a tested-version guarantee). [Platform notes](https://kyverno.io/docs/installation/platform-notes/) and [generate lifecycle](https://kyverno.io/docs/policy-types/cluster-policy/generate/) are primary guidance. Synchronization, deletion of a source and a source no longer matching are distinct. Proposed synchronized per-Service generation plus existing-resource generation is **provisional**: Task 8 must prove create/update/delete/disable/remove/preexisting behavior against this release and choose explicit cleanup/source validation if necessary. Discovery must not be enabled without that proof.

Later-task tools remain absent from `[tools]` to keep `mise install` Go-only. Add exact pins in consuming tasks. Task 4 verifies the Compose image/schema/persistence subset below; Kubernetes charts/PVCs/controllers/scaling and Kyverno lifecycle are **not verified here**. Recheck selections when consumed; upstream release metadata can have timestamps later than the dated planning documents.

## CI action provenance and review

Resolved upstream release tags to commits through the GitHub commits API, then read `action.yml` at each exact commit before selecting inputs:

- `actions/checkout` **v7.0.1**, `3d3c42e5aac5ba805825da76410c181273ba90b1`: <https://github.com/actions/checkout/blob/3d3c42e5aac5ba805825da76410c181273ba90b1/action.yml>. Node 24; disable credential persistence and global safe-directory modification.
- The local composite action `.github/actions/setup-mise/action.yaml` wraps `jdx/mise-action` **v5.2.2**, `8418fedb1110381a0d3ba51a7a415d7c1b884550`, unchanged; workflows call `./.github/actions/setup-mise` after checkout: <https://github.com/jdx/mise-action/blob/8418fedb1110381a0d3ba51a7a415d7c1b884550/action.yml>. Node 24; exact mise version, Go-only install, cache disabled, no persisted token or OIDC. Download verification is described by the action; bundled JavaScript is not independently audited here.

CI uses read-only contents permissions, `pull_request` (not `pull_request_target`), a bounded timeout, and the exact local `mise run validate` command. Linux amd64 CI is configured, **not executed by this local task**; the controller must observe its result after opening the PR. Local verification evidence is recorded in the Task 1 report.

## Task 4 consumed Compose images and ARM64 runtime evidence

Initial research **2026-10-25 UTC**, final host acceptance **2026-10-26 UTC**
(after an interactive-session pause). `docker buildx imagetools inspect <tag>` resolved
these multi-platform index digests; every index includes **linux/amd64** and
**linux/arm64** (Go/Debian/PostgreSQL label ARM64 as arm64/v8). Tags are kept
readable but deployment/build uses these immutable digests, never `latest`:

- Go build: `golang:1.27.3-bookworm@sha256:972b4110ba0f47539a975c31e52c64765e1ff19f2d74469ae4a595c6676e80a5`.
- App runtime: `debian:bookworm-slim@sha256:7ff6ba31527b9e336162fa061746c366c4b2032f4d2cff2078bea43f78efe3f8` (digest pins the otherwise mutable Debian tag).
- PostgreSQL: `postgres:16.15@sha256:9e2cf6287f25c3151f4c9421ccce2b17f3e60061c111a29165e0d2cdd9ff0a97`.
- Server: `temporalio/server:1.32.0@sha256:c3e752127759616bb1615e0f9ba0e21635aeb5fdeb922de4f371c350955f46ae`.
- Matching schema tools: `temporalio/admin-tools:1.32.0@sha256:a9f84fb9a374b2374fe2e67c8efc0468ff3f1c66c8a0b14597ec86e349e62bca`.
- Independently released UI: `temporalio/ui:2.48.0@sha256:5bc8712de27029b64a9cad156221b082a43ae27bfb108fc1743076eaabf1437f`.

Sources: [Go official Docker image](https://hub.docker.com/_/golang),
[Debian](https://hub.docker.com/_/debian), [PostgreSQL](https://hub.docker.com/_/postgres),
[Temporal server release1.32.0](https://github.com/temporalio/temporal/releases/tag/v1.32.0),
[server packaging](https://github.com/temporalio/docker-builds/blob/main/server.Dockerfile),
[admin packaging](https://github.com/temporalio/docker-builds/blob/main/admin-tools.Dockerfile),
[UI release2.48.0](https://github.com/temporalio/ui/releases/tag/v2.48.0).
Packaging main-branch sources are research context, not immutable deployment
proof; inspecting/running the digest-pinned images is the packaging evidence.
The [tagged server config loader](https://github.com/temporalio/temporal/blob/v1.32.0/common/config/loader.go)
supports native `# enable-template` and `TEMPORAL_SERVER_CONFIG_FILE_PATH`.
The [tagged postgres12 config](https://github.com/temporalio/temporal/blob/v1.32.0/config/development-postgres12.yaml)
and [matching schemas](https://github.com/temporalio/temporal/tree/v1.32.0/schema/postgresql/v12)
are the upstream persistence configuration source. No auto-setup image or SQLite
shortcut is involved.

Measured on Darwin ARM64 host, Docker29.5.3 Linux/aarch64 VM (12 CPUs,
8,321,232,896 bytes RAM), Compose5.1.4:

- Both Go images built and ran as `linux/arm64`, UID/GID65532. Server binary
  reports1.32.0. Admin image's `temporal-sql-tool --help` reports1.32.0 and includes
  `/etc/temporal/schema/postgresql/v12/{temporal,visibility}/versioned`.
  Its separate CLI is1.9.0 (CLI's embedded dev-server/UI version is not our
  independently selected Compose UI).
- PostgreSQL `SHOW server_version`: **16.15 (Debian16.15-1.pgdg13+2)**. Matching
  `postgres12` migrations set default DB schema **1.19**, visibility DB **1.14**.
  Schema setup rerun reports current version greater than0.0 and zero updates;
  original completed history remains byte-identical through the API.
- SDK1.50.0 + server1.32.0 completed the actual diamond, identity-only sorted
  result, plan hash, body400 and durable keyed replay after completion and
  orchestrator restart. A fresh isolated project proved Temporal/PostgreSQL
  restart, container replacement, repeatable schemas and Compose down/up retain
  the exact workflowId/runId/planHash/result. Only the isolated fixture's explicit
  reset removed history (GET404), then a fresh diamond succeeded.
- Evidence commands: `mise run validate`, `docker compose -f deploy/compose.yaml
  config --quiet`, `mise run compose:up`, `mise run compose:smoke`,
  `mise run compose:persistence`, `mise run compose:down`. Detailed RED/GREEN,
  failures/fixes and cleanup: `.superpowers/sdd/2026-10-17-temporal-k8s-orchestration/task-4-report.md`.
  These are real host-Docker results, **not a Linux amd64 CI execution claim**.

`.github/workflows/compose.yaml` reuses reviewed checkout/setup-mise pins and
runs the same local image-build/up/smoke/persistence tasks on Linux amd64,
with read-only permissions,25-minute job budget and unconditional scoped
cleanup. No new external action was added. **Linux amd64 runtime gate remains
pending controller observation of CI.** Kubernetes/controller gates remain open.
