#!/usr/bin/env bash
# Poll the running API; never use the expectation as an execution registry.
set -euo pipefail
api=${1:?API URL required}
expected=${2:?expected worker JSON required}
jq -e 'type == "array" and length > 0 and all(.[];
  (.name | type == "string") and (.taskQueue | type == "string") and
  (.workflowType | type == "string") and (.dependencies == null or (.dependencies | type == "array")))' "$expected" >/dev/null || {
  echo "Missing/malformed expected registrations: $expected; repair bootstrap state before retrying" >&2; exit 1;
}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
start=$(date +%s)
deadline=$((start + 120))
code=000
printf '{}\n' >"$work/response"
: >"$work/error"
while true; do
  remaining=$((deadline - $(date +%s)))
  ((remaining > 0)) || break
  timeout=2
  ((remaining >= timeout)) || timeout=$remaining
  code=$(curl -sS --max-time "$timeout" -o "$work/response" -w '%{http_code}' "$api/graph" 2>"$work/error") || code=000
  if [[ $code == 200 ]] && jq -e --slurpfile expected "$expected" \
    '.synced == true and .valid == true and .problems == [] and (.plan.nodes | sort_by(.name)) == ($expected[0] | sort_by(.name))' "$work/response" >/dev/null 2>&1; then
    echo 'Complete worker graph discovered'
    exit 0
  fi
  remaining=$((deadline - $(date +%s)))
  ((remaining > 0)) || break
  sleep 1
 done
echo "Graph convergence timed out after 120 seconds (last HTTP status: $code)" >&2
# Print only explicit public graph diagnostics, never raw arbitrary HTTP bodies.
if ! jq -e 'type == "object"' "$work/response" >/dev/null 2>&1; then printf '{}\n' >"$work/response"; fi
jq --slurpfile expected "$expected" '
  (.plan.nodes // []) as $actual |
  {problems:(.problems // []),
   missing:([$expected[0][].name] - [$actual[].name]),
   extra:([$actual[].name] - [$expected[0][].name]),
   mismatched:[$expected[0][] as $want | $actual[] | select(.name == $want.name and . != $want) | .name]}' "$work/response" >&2
[[ $code != 000 ]] || echo 'Last graph request failed or timed out' >&2
exit 1
