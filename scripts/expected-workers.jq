# Input is kubectl's JSON document stream, slurped. This is an expectation
# projection only; Go's plan.Build remains the graph validation authority.
def trim: gsub("^[\\s\u0085]+|[\\s\u0085]+$"; "");
[.[] | if .kind == "List" then .items[] else . end] as $resources |
[$resources[] | select(.kind == "Namespace" and .metadata.labels["temporal-demo.io/worker-namespace"] == "true") | .metadata.name] as $namespaces |
[$resources[] | select(.kind == "Service" and .metadata.annotations["temporal-worker"] == "true") |
 select(.metadata.namespace as $ns | $namespaces | index($ns)) |
 {name: .metadata.name, taskQueue: (.metadata.name + ".v1.temporal"), workflowType: "ExampleChildWorkflow",
 dependencies: ((.metadata.annotations["temporal-worker-run-after"] // "") | trim | if . == "" then null else split(",") | map(trim) | unique end)}] |
sort_by(.name) | if length == 0 then error("no intended worker registrations") else . end
