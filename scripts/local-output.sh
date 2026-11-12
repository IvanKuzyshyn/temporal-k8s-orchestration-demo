#!/usr/bin/env bash
# Presentation only: do not wrap phase commands in conditionals (which disable
# Bash errexit inside functions). The lifecycle's EXIT trap reports failures.
demo_output_start() {
  export DEMO_PROGRESS=true
  DEMO_UP_LOG=$1
  exec 3>&1 4>&2
  : >"$DEMO_UP_LOG"
  exec >>"$DEMO_UP_LOG" 2>&1
}
demo_phase() {
  DEMO_PHASE=$*
  if [[ ${DEMO_PROGRESS:-} == true ]]; then
    printf '  → %s' "$*" >&3
    printf '\n== %s ==\n' "$*"
  else
    printf '  → %s\n' "$*"
  fi
}
# Called explicitly after a phase succeeds, never from an EXIT trap.
demo_phase_done() {
  if [[ ${DEMO_PROGRESS:-} == true ]]; then
    printf ' ✅\n' >&3
  else
    printf '  → %s ✅\n' "$DEMO_PHASE"
  fi
}
demo_command() {
  printf '%s\n' "$*" >&3
}
demo_output_finish() {
  if [[ ${DEMO_PROGRESS:-} == true && $1 != 0 ]]; then
    printf '\nStartup failed. Full startup output (%s):\n' "$DEMO_UP_LOG" >&4
    cat "$DEMO_UP_LOG" >&4
  fi
}
