#!/bin/sh
set -euo pipefail
for i in $(seq 1 30); do
  if temporal operator cluster health --command-timeout 2s --address temporal:7233 >/dev/null 2>&1; then break; fi
  if [ "$i" = 30 ]; then echo 'Temporal health timed out' >&2; exit 1; fi
  sleep 1
done
if ! temporal operator namespace describe --command-timeout 5s --address temporal:7233 --namespace default >/dev/null 2>&1; then
  temporal operator namespace create --command-timeout 5s --address temporal:7233 --namespace default --retention 72h
fi
temporal operator namespace describe --command-timeout 5s --address temporal:7233 --namespace default >/dev/null
# Frontend persistence sees a freshly created namespace before matching's cache.
# Verify the actual polling destination recognizes it before releasing workers.
for i in $(seq 1 30); do
  if temporal task-queue describe --command-timeout 2s --address temporal:7233 --namespace default --task-queue orchestrator.v1.temporal --task-queue-type workflow >/dev/null 2>&1; then exit 0; fi
  if [ "$i" = 30 ]; then echo 'Namespace matching readiness timed out' >&2; exit 1; fi
  sleep 1
done
