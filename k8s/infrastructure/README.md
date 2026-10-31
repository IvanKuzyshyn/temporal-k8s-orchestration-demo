# Infrastructure inputs

- `kind.yaml`: single-node kind **input**, never an apply resource.
- `temporal.yaml`: Namespace, PostgreSQL Service/StatefulSet/PVC template,
  ConfigMaps, schema Job, Temporal Service/Deployment, default-namespace Job and
  UI. ConfigMaps match `deploy/temporal/`; tests keep these inputs equal.
- `keda.yaml`: complete pinned baseline controllers/RBAC/CRDs, vendored from the
  chart below. `keda-values.yaml` reproduces that committed render.
- `kyverno-values.yaml`: runtime inputs for the pinned Kyverno chart. Its generated
  manifest is temporary, not committed. No scaling/admission policies are added.

`bash scripts/k8s-up.sh` renders Kyverno once and strictly validates that artifact
before Kyverno mutations. Native kubectl label selectors apply its Namespace,
CRDs and remaining controller resources from **that same file**. Both Kyverno CRD
subcharts receive `temporal-demo.io/phase=crds` through supported chart values;
not all upstream CRDs otherwise have component labels. Handwritten YAML/templates
stay block style; upstream temporary output does not need normalization.

Temporal/KEDA retain their labeled bootstrap phases: namespaces, PostgreSQL/config
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
are unchanged. Kyverno startup, ordinary Go tests and cumulative offline schema
validation share `scripts/render-kyverno.sh`. On cache miss it downloads only the
exact upstream archive with finite connect/total/retry limits, verifies SHA256,
then stores it in ignored `.local/charts`. **Every cached use rechecks SHA256**;
a mismatch fails rather than silently replacing bytes. A warm archive/schema
cache allows validation without network; a cold cache requires upstream access.
Neither rendering nor strict schema validation needs a live cluster.

From the repository root:

```sh
mise exec -- bash scripts/render-kyverno.sh > .local/kyverno-render.yaml
mise run validate:manifests
```

The helper explicitly renders release/namespace `kyverno`, Kubernetes `1.35.8`,
existing values and `--include-crds --no-hooks --skip-tests`, then adds the release
Namespace. It emits no deployable output on checksum/download/template failure.
KEDA's committed render can be reproduced after verifying its archive hash:

```sh
mise exec -- helm template keda .local/charts/keda-2.21.0.tgz \
  --namespace keda --kube-version 1.35.8 --include-crds --no-hooks --skip-tests \
  -f k8s/infrastructure/keda-values.yaml > .local/keda-render.yaml
```

The committed KEDA file adds a Namespace and phase labels (`namespace`, `crds`,
`controller`) and expands nonempty collections to block style. No formatter,
YAML parser, Helm repository update or Helm-managed install/upgrade is required.

Helm tests, pre-delete teardown Jobs/RBAC and post-upgrade migration hooks are
intentionally excluded: they are lifecycle operations, not always-applied
infrastructure. Pre-delete hooks would remove live webhooks and scale controllers
down. This is the **same pinned release**, without policies to migrate; future
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

Strict validation covers every committed KEDA and generated Kyverno resource,
including CRD definitions. The pinned core catalog omits the CRD-definition
schema, so its strict standalone schema is derived from Kubernetes **1.35.8**;
see [schemas/provenance](../schemas/README.md). Custom instances use exact
release-derived schemas. Unknown kinds fail; no upstream resources are skipped.
