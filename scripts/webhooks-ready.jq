.items as $items |
["keda-admission", "kyverno-policy-validating-webhook-cfg",
 "kyverno-policy-mutating-webhook-cfg", "kyverno-verify-mutating-webhook-cfg",
 "kyverno-cleanup-validating-webhook-cfg"] |
all(.[]; . as $name |
  any($items[]; .metadata.name == $name and
    ((.webhooks // []) | length) > 0 and
    all(.webhooks[]; (.clientConfig.caBundle // "" | length) > 0))) and
all($items[] | select(.metadata.name | test("^(keda|kyverno)")) | .webhooks[]?;
  (.clientConfig.caBundle // "" | length) > 0)
