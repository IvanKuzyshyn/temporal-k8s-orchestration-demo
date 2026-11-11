# Infrastructure inputs

Run `mise run up` from the repository root. Infrastructure helpers are internal
implementation details, not additional supported deployment modes.

- `kind.yaml`: single-node kind creation input, never a Kubernetes apply resource.
- `temporal.yaml`: PostgreSQL Service/StatefulSet/PVC template, Temporal/UI,
  schema/default-namespace Jobs and bootstrap/configuration ConfigMaps.
- `versions.env`: pinned node/controller versions and chart SHA256 checksums.
- `keda-values.yaml`, `kyverno-values.yaml`: pinned upstream controller values,
  including image digests. Full controller output is generated, not checked in.

## Bootstrap ordering

`k8s-up.sh` renders both controllers before deployment mutation. It installs CRDs
with server-side apply (too large for last-applied annotations), waits for CRD
establishment, installs ordinary controller resources with client-side apply,
and waits for controller readiness and webhook CA injection. It does not use
force-conflicts. Native labels partition the same rendered artifacts into phases.

Temporal bootstraps PostgreSQL/config → completed schema Job → ready Temporal →
completed `default` namespace Job → UI. SQL bootstrap helpers are ConfigMaps in
`temporal.yaml`, not duplicated host scripts. Immutable PostgreSQL credentials
are paired with retained storage; a PVC without its Secret is an error, never
permission to generate a replacement password.

Then policies are installed, the full app chart is rendered, Namespaces are
applied and the same app artifact is strictly server-dry-run validated before
bootstrap apply. Initial workers are zero; steady-state resources omit replicas
and remove old replica ownership from last-applied annotations before handoff.
A successful render's expected registrations are persisted for live graph checks.
Offline Go manifest tests retain full graph validation; no graph CLI is used.

An initialized `up` resumes instead of rerunning bootstrap/schema Jobs. A failed
partial bootstrap can be retried; installed immutable credentials/storage remain.
Foreign clusters are not adopted. Upgrades require explicit destructive recreate
from the owning checkout, with history loss disclosed in the project README.

## Controller rendering

`scripts/render-controller.sh keda|kyverno` is a fixed two-controller helper. It
verifies exact chart archive SHA256 on downloads **and cache hits**. Download and
retry bounds are finite; a checksum mismatch fails without silently replacing
cached bytes. Cache: ignored `.local/charts`. Cold validation requires network;
warm chart rendering requires no Docker daemon or Kubernetes API.

```sh
mise exec -- bash scripts/render-controller.sh keda > /tmp/demo-keda.yaml
mise exec -- bash scripts/render-controller.sh kyverno > /tmp/demo-kyverno.yaml
mise run validate
```

Pinned charts: KEDA 2.21.0 and Kyverno 3.9.2 (app 1.19.2). Helm renders release and
namespace `keda` or `kyverno` for Kubernetes 1.35.8, with CRDs included and hooks/
chart tests excluded. Controller-generated webhooks and serving Secrets remain
controller-owned. Pre-delete/migration hooks are not always-applied resources;
future version upgrades must explicitly assess them.

These are kubectl-managed resources, **not Helm releases**. Do not use Helm
upgrade/uninstall against them. Strict server validation uses the real ready
Kubernetes API, not a local schema catalog or client-validation fallback.
Developer fast validation includes Helm values schema and actual manifest graph
checks; disposable lifecycle CI exercises deployment/API acceptance.

`down` retains the node/PVC and credentials. `destroy` deletes the owned node and
history, leaving unrelated resources and shared caches untouched. See
[versions](../../docs/versions.md) and [testing](../../docs/testing.md).
