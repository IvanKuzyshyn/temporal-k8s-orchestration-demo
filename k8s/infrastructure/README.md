# Plain infrastructure manifests

- `kind.yaml`: single-node kind **input**, not a Kubernetes resource.
- `temporal.yaml`: Namespace, PostgreSQL Service/StatefulSet/PVC template,
  ConfigMaps, schema Job, Temporal Service/Deployment, default-namespace Job and
  UI. ConfigMap data is copied verbatim from `deploy/temporal/`; tests keep those
  inputs equal. Images/tools and schemas remain the tested Compose versions.
- `keda.yaml`, `kyverno.yaml`: full baseline controller resources and CRDs,
  vendored from the pinned charts below. No scaling/admission policies are added.

`bash scripts/k8s-up.sh` uses native `kubectl apply -l temporal-demo.io/phase=…`
for explicit ordering; it does not parse/split manifests. Namespaces come first,
then PostgreSQL/config → completed schema Job → ready Temporal → completed
namespace Job → UI. Controller CRDs use server-side apply (too large for
client-side last-applied annotations), then establishment, ordinary controller
resources, Deployment readiness and required webhook CA injection. Jobs are
recreated sequentially on reruns; storage and the installed immutable Secret are
not. A retained PVC without its Secret is an error, not permission to rotate it.

## Controller provenance and reproduction

Pinned Helm **4.3.1**, Kubernetes render version **1.35.8**:

- KEDA chart/app **2.21.0/2.21.0**, archive
  <https://kedacore.github.io/charts/keda-2.21.0.tgz>, SHA256
  `a7da56bf41a33dad9d2fc82a6fdecee552fff947d80bf1e1dbcf4db0f9e26dee`.
- Kyverno chart/app **3.9.2/1.19.2**, archive
  <https://kyverno.github.io/kyverno/kyverno-3.9.2.tgz>, SHA256
  `f6fe7184544d5e2ee33a77808c09e67aa603ce6560cc197038fde31d727c861e`.

`versions.env` locks those bytes; the corresponding `*-values.yaml` files are
retained **only to reproduce these renders**. All deployed controller/init image
digests are unchanged. Unused Helm hook image overrides were removed.
Provisioning consumes the committed YAML, not chart downloads or Helm releases.

From the repository root, download archives into ignored `.local/charts` if
absent, verify their SHA256 against `versions.env` with `openssl dgst -sha256`,
then render:

```sh
mise exec -- helm template keda .local/charts/keda-2.21.0.tgz \
  --namespace keda --kube-version 1.35.8 --include-crds --no-hooks --skip-tests \
  -f k8s/infrastructure/keda-values.yaml > .local/keda-render.yaml
mise exec -- helm template kyverno .local/charts/kyverno-3.9.2.tgz \
  --namespace kyverno --kube-version 1.35.8 --include-crds --no-hooks --skip-tests \
  -f k8s/infrastructure/kyverno-values.yaml > .local/kyverno-render.yaml
```

The vendored files preserve Helm `# Source:` comments. Each has a leading
Namespace with phase `namespace`; rendered resource metadata gets phase `crds`
for CustomResourceDefinition and `controller` otherwise. Nonempty flow
collections were expanded to two-space block YAML without changing scalar types.
For a one-off regeneration, an already installed MikeFarah/yq can perform this
normalization (it is **not** a project/runtime/CI dependency):

```sh
# Prepend the commented provenance header and explicit Namespace from the
# committed file to this normalized render, separated by ---.
yq eval -P '.metadata.labels."temporal-demo.io/phase" = "controller" |
  (select(.kind == "CustomResourceDefinition").metadata.labels."temporal-demo.io/phase") = "crds" |
  (.. | select(tag == "!!map" or tag == "!!seq")) style = ""' .local/keda-render.yaml
# Same transformation for .local/kyverno-render.yaml.
mise run validate:manifests
```

Helm tests, pre-delete teardown Jobs/RBAC and post-upgrade migration hooks are
intentionally excluded: they are Helm lifecycle operations, not always-applied
infrastructure. Running pre-delete hooks during up would remove live webhooks
and scale down controllers. This is the **same pinned release**, without policies
to migrate; future upgrades must explicitly assess migrations, not silently omit
them. The controller-generated Kyverno webhook configurations and serving
Secrets remain controller-owned, not copied into Git.

## Migration from the earlier PR8 layout

The retained same-version Helm installation can be adopted in place. CRD SSA
shares matching values, without `--force-conflicts`. Ordinary resources use
client-side apply to avoid SSA conflicts on API-defaulted fields previously
owned by Helm; first adoption emits expected missing-last-applied warnings.
No uninstall, replacement, PVC deletion or Secret update is needed. Legacy Helm
release records/completed hook Jobs may remain inert; **do not run Helm upgrade
or uninstall against this now kubectl-managed demo**. `k8s:down` deletes only the
named demo cluster and all its history; it is the explicit destructive reset.

## Validation and formatter research

Strict validation covers all committed controller resources, including CRD
definitions. The pinned core catalog omits the CRD-definition schema, so its
strict standalone schema is derived from Kubernetes **1.35.8** itself; see
[schemas/provenance](../schemas/README.md). Actual CRD instances additionally
need their exact release-derived schemas; no unknown kind is blanket-ignored.

[google/yamlfmt](https://github.com/google/yamlfmt/blob/main/docs/config-file.md)
provides formatting and `-lint`; documented `force_array_style` does not promise
to force every mapping into block style.
[MikeFarah/yq style](https://mikefarah.gitbook.io/yq/operators/style) documents
`-P`/recursive style reset for expanding flow collections. Raw Helm templates
are **not ordinary YAML**: never blindly run either formatter over them. The
one-off template conversion preserved Helm expressions, comments and scalar
types; real rendered-manifest tests check nonempty collections are block style.
No formatter dependency, configuration or CI job was added.
