# Pinned schema provenance

Core schemas: Kubernetes **1.35.8**, strict standalone JSON schemas at
`https://raw.githubusercontent.com/yannh/kubernetes-json-schema/47163ead3738a996c3e30be9691bb37b8071d9d3/v1.35.8-standalone-strict/`.
The validator does not use kubeconform's mutable default schema location.
Downloaded core files cache under ignored `.local/schema-cache`; URLs contain
the immutable revision. Network/schema-download failures fail validation.

Custom schemas below are the unmodified `openAPIV3Schema` objects from the exact
chart releases, serialized with Go's deterministic JSON encoder (newline included):

- `keda.sh/scaledobject_v1alpha1.json`: KEDA chart **2.21.0**, app **2.21.0**,
  archive SHA256 `a7da56bf41a33dad9d2fc82a6fdecee552fff947d80bf1e1dbcf4db0f9e26dee`;
  schema SHA256 `bf154284ef70975b9c9032a22fbe52feb76ee99c8341703b05f66938329196d5`.
- `kyverno.io/clusterpolicy_v1.json`: Kyverno chart **3.9.2**, app **1.19.2**,
  archive SHA256 `f6fe7184544d5e2ee33a77808c09e67aa603ce6560cc197038fde31d727c861e`;
  schema SHA256 `fde68e1f0afaca4240e467cf438e4948c38c40d18c98c590b73230572a77e812`.

Sources: <https://kedacore.github.io/charts/index.yaml>,
<https://kyverno.github.io/kyverno/index.yaml>. Included subcharts are covered by
the archive hash. These are upstream structural schemas, not hand-written
approximations; free-form fields/patterns retain upstream semantics. kubeconform
`-strict` selects strict core schemas; it does not turn custom pattern fields
into a substitute for Kubernetes admission. Custom-schema fixtures in
`k8s/schema-tests/custom.yaml` are **never deployed** in static mode. Unknown
kinds fail, and known invalid custom fields are covered by real validator tests.
No `-ignore-missing-schemas` or mutable catalog fallback is used.

Reproduce from the repository root (no cluster needed), verifying archive hashes
against `k8s/infrastructure/versions.env` before extraction:

```sh
mkdir -p .local/schema-source
mise exec -- helm pull keda --repo https://kedacore.github.io/charts --version 2.21.0 --destination .local/schema-source
mise exec -- helm pull kyverno --repo https://kyverno.github.io/kyverno --version 3.9.2 --destination .local/schema-source
openssl dgst -sha256 .local/schema-source/*.tgz
mise exec -- helm template keda .local/schema-source/keda-2.21.0.tgz --namespace keda > .local/schema-source/keda.yaml
mise exec -- helm template kyverno .local/schema-source/kyverno-3.9.2.tgz --namespace kyverno > .local/schema-source/kyverno.yaml
mise exec -- go run ./cmd/export-crd-schemas keda.sh ScaledObject v1alpha1 < .local/schema-source/keda.yaml > .local/schema-source/scaledobject.json
mise exec -- go run ./cmd/export-crd-schemas kyverno.io ClusterPolicy v1 < .local/schema-source/kyverno.yaml > .local/schema-source/clusterpolicy.json
cmp .local/schema-source/scaledobject.json k8s/schemas/keda.sh/scaledobject_v1alpha1.json
cmp .local/schema-source/clusterpolicy.json k8s/schemas/kyverno.io/clusterpolicy_v1.json
mise run validate:manifests
```

## CRD-definition schema (Kubernetes 1.35.8)

The pinned core catalog omits **CustomResourceDefinition** itself. Complete
vendored KEDA/Kyverno manifests therefore also use
`apiextensions.k8s.io/customresourcedefinition_v1.json`, derived from the official
Kubernetes **v1.35.8** Swagger model, not from an unrelated custom-kind catalog:

- Source: `https://raw.githubusercontent.com/kubernetes/kubernetes/1c2e10a409eb1b03f2f28f401ce935312e20d9fb/api/openapi-spec/swagger.json`
  (resolved v1.35.8 commit); SHA256
  `483500149ee52ce5753d75f5639101d985bb4f5e902cc05b1ba7627465d62446`.
- Root model: `io.k8s.apiextensions-apiserver.pkg.apis.apiextensions.v1.CustomResourceDefinition`.
  Its transitive referenced definitions are bundled unchanged; strict unknown-field
  handling adds `additionalProperties: false` to typed object models with named
  properties and no existing additionalProperties rule. Free-form maps and
  explicit additionalProperties schemas retain their upstream semantics.
- Serialized as sorted compact JSON plus newline, SHA256
  `e06421829d052f8e57263f4d23e5cfbe9273871c02204bce973086d226cdc305`.

For one-off reproduction, download/check the source into `.local/swagger.json`,
then run this standard-library-only Python snippet (not a project dependency):

```python
import json
models = json.load(open('.local/swagger.json'))['definitions']
name = 'io.k8s.apiextensions-apiserver.pkg.apis.apiextensions.v1.CustomResourceDefinition'
refs = set()
def collect(v):
    if isinstance(v, dict):
        if isinstance(v.get('$ref'), str):
            ref = v['$ref'].removeprefix('#/definitions/')
            if ref not in refs:
                refs.add(ref)
                collect(models[ref])
        for child in v.values(): collect(child)
    elif isinstance(v, list):
        for child in v: collect(child)
collect(models[name])
schema = dict(models[name], definitions={r: models[r] for r in sorted(refs)})
def strict(v):
    if isinstance(v, dict):
        if v.get('type') == 'object' and 'properties' in v and 'additionalProperties' not in v:
            v['additionalProperties'] = False
        for child in v.values(): strict(child)
    elif isinstance(v, list):
        for child in v: strict(child)
strict(schema)
with open('.local/customresourcedefinition_v1.json', 'w') as out:
    out.write(json.dumps(schema, sort_keys=True, separators=(',', ':')) + '\n')
```

The real validator tests cover complete controller manifests and positive/negative
CRD-definition field types as well as the existing ScaledObject/ClusterPolicy
schemas. No kinds are skipped. When later tasks introduce additional CRD kinds,
add schemas from their exact consumed release before accepting them.
