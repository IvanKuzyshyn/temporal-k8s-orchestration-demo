#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/kind-common.sh"
assert_context
echo "Scoped diagnostics: context=$CONTEXT (schema failure evidence is credential-redacted)" >&2
k get pods,pvc -A >&2 || true
k get events -A --field-selector type=Warning >&2 || true
for ns in temporal-demo-orchestrator temporal-demo-worker-a temporal-demo-worker-b temporal-demo-worker-c temporal-demo-worker-d; do
  k -n "$ns" logs -l app --all-containers --tail=20 --max-log-requests=5 >&2 || true
done
# Buffer schema evidence privately; nothing from SQL tools is released until
# installed credentials are available and the entire buffer is redacted.
umask 077
evidence=$(mktemp -d)
trap 'rm -rf "$evidence"' EXIT
trap 'exit 143' TERM
trap 'exit 130' INT
k -n temporal get job temporal-schema --ignore-not-found -o json > "$evidence/job.json" 2>/dev/null || { echo 'Schema Job evidence unavailable' >&2; exit 1; }
[[ -s $evidence/job.json ]] || exit 0
failed=$(jq -r '((.status.failed // 0) > 0) or any(.status.conditions[]?; (.type == "Failed" or .type == "FailureTarget") and .status == "True")' "$evidence/job.json" 2>/dev/null) || { echo 'Schema Job status unavailable' >&2; exit 1; }
[[ $failed == true ]] || exit 0
k -n temporal get secret postgres-credentials -o json > "$evidence/secret.json" 2>/dev/null || { echo 'Schema evidence withheld: credential lookup failed' >&2; exit 1; }
jq -e 'try (.data.POSTGRES_PASSWORD as $encoded | ($encoded | type) == "string" and ($encoded | length) > 0 and (($encoded | @base64d | @base64) == $encoded) and ($encoded | @base64d | length) > 0) catch false' "$evidence/secret.json" >/dev/null 2>&1 || { echo 'Schema evidence withheld: credential validation failed' >&2; exit 1; }
k -n temporal get pods -l job-name=temporal-schema -o json > "$evidence/pods.json" 2>/dev/null || { echo 'Schema Pod evidence unavailable' >&2; exit 1; }
jq '{job: .metadata.name, failed: .status.failed, conditions: [.status.conditions[]? | {type, status, reason}]}' "$evidence/job.json" > "$evidence/raw" 2>/dev/null
jq '{pods: [.items[] | {name: .metadata.name, phase: .status.phase, containers: [.status.containerStatuses[]? | {name, terminated: (.state.terminated | {reason, exitCode, signal, startedAt, finishedAt})}]}]}' "$evidence/pods.json" >> "$evidence/raw" 2>/dev/null
k -n temporal logs job/temporal-schema --container=schema --tail=100 --limit-bytes=16384 --pod-running-timeout=10s >> "$evidence/raw" 2>&1 || true
jq -Rrs --slurpfile secret "$evidence/secret.json" '
  . as $text | $secret[0].data.POSTGRES_PASSWORD as $encoded |
  [$encoded, ($encoded | @base64d)] | sort_by(length) | reverse as $tokens |
  reduce $tokens[] as $token ($text; split($token) | join("[REDACTED]"))
' "$evidence/raw" > "$evidence/redacted" 2>/dev/null || { echo 'Schema evidence withheld: redaction failed' >&2; exit 1; }
cat "$evidence/redacted" >&2
