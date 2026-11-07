# Infrastructure inputs

- `kind.yaml`: single-node kind **input**, never an apply resource.
- `temporal.yaml`: Namespace, PostgreSQL Service/StatefulSet/PVC template,
  ConfigMaps, schema Job, Temporal Service/Deployment, default-namespace Job and
  UI. ConfigMaps match `deploy/temporal/`; tests keep these inputs equal.
- `keda-values.yaml` and `kyverno-values.yaml`: runtime inputs for the pinned
  upstream charts. Complete generated controllers/RBAC/CRDs are temporary, not
  committed. No scaling/admission policies are added.

`bash scripts/k8s-up.sh` renders both controllers once and strictly validates
those artifacts before resource mutations. Native kubectl label selectors apply
Namespaces, CRDs and controller resources from **those same files**. Kyverno's
both CRD subcharts receive `temporal-demo.io/phase=crds` through supported values.
KEDA 2.21.0 supports `additionalLabels` for controllers (including Pod templates),
but its CRD helper does not consume them. Its six CRDs are selected by native
`app.kubernetes.io/part-of=keda-operator,!app.kubernetes.io/instance`; all other
chart resources have `instance=keda` and receive `temporal-demo.io/phase=controller`.
Tests check these partitions against real chart output. No YAML rewriting is needed.
Handwritten YAML/templates stay block style; upstream temporary output does not
need normalization.

Temporal's labeled bootstrap phases remain namespaces, PostgreSQL/config
→ completed schema Job → ready Temporal → completed namespace Job → UI.
Controller CRDs use server-side apply (too large for client-side last-applied
annotations), then establishment, ordinary controller resources, Deployment
readiness and required webhook CA injection. Jobs are recreated sequentially on
reruns; storage and the installed immutable Secret are not. A retained PVC without
its Secret is an error, not permission to rotate it.

## Controller provenance and reproduction

Pinned Helm **4.3.1**, Kubernetes render version **1.35.8**:

- KEDA chart/app **2.21.0/2.21.0**, archive
  <https://kedacore.github.io/charts/keda-2.21.0.tgz>, SHA256
  `a7da56bf41a33dad9d2fc82a6fdecee552fff947d80bf1e1dbcf4db0f9e26dee`.
- Kyverno chart/app **3.9.2/1.19.2**, archive
  <https://kyverno.github.io/kyverno/kyverno-3.9.2.tgz>, SHA256
  `f6fe7184544d5e2ee33a77808c09e67aa603ce6560cc197038fde31d727c861e`.

`versions.env` locks archive bytes. All deployed controller/init image digests
are unchanged. Startup, ordinary Go tests and cumulative offline schema validation
share `scripts/render-controller.sh keda|kyverno`, a fixed two-controller selector,
not a generic chart framework. On cache miss it downloads only the exact upstream
archive with finite connect/total/retry limits, verifies SHA256, then stores it in
ignored `.local/charts`. **Every cached use rechecks SHA256**; a mismatch fails
rather than silently replacing bytes. A warm archive/schema cache allows validation
without network; a cold cache requires upstream access. Neither rendering nor
strict schema validation needs a live cluster.

From the repository root:

```sh
mise exec -- bash scripts/render-controller.sh keda > .local/keda-render.yaml
mise exec -- bash scripts/render-controller.sh kyverno > .local/kyverno-render.yaml
mise run validate:manifests
```

The helper explicitly renders release/namespace `keda` or `kyverno`, Kubernetes
`1.35.8`, existing values and `--include-crds --no-hooks --skip-tests`, then adds
the release Namespace. It emits no deployable output on checksum/download/template
failure. No formatter, YAML parser, Helm repository update or Helm-managed
install/upgrade is required.

Helm tests, pre-delete teardown Jobs/RBAC and post-upgrade migration hooks are
intentionally excluded: they are lifecycle operations, not always-applied
infrastructure. Pre-delete hooks would remove live webhooks and scale controllers
down. These are the **same pinned releases**, without policies to migrate; future
upgrades must explicitly assess migrations. Controller-generated Kyverno webhook
configurations and serving Secrets remain controller-owned, not copied into Git.

## Same-version adoption and validation

A retained same-version Helm installation can be adopted in place. CRD SSA shares
matching values, without `--force-conflicts`. Ordinary resources use client-side
apply to avoid SSA conflicts on API-defaulted fields previously owned by Helm;
first adoption can emit missing-last-applied warnings. No uninstall, replacement,
PVC deletion or Secret update is needed. Legacy Helm records/completed hook Jobs
may remain inert; **do not run Helm upgrade or uninstall against this now
kubectl-managed demo**. `k8s:down` is the explicit destructive cluster/history reset.

Strict validation covers every generated KEDA and Kyverno resource, including
CRD definitions. The pinned core catalog omits the CRD-definition schema, so its
strict standalone schema is derived from Kubernetes **1.35.8**; see
[schemas/provenance](../schemas/README.md). Custom instances use exact
release-derived schemas, which remain committed. Unknown kinds fail; no upstream
resources are skipped.
