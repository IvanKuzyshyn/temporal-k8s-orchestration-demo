#!/bin/sh
set -euo pipefail
for store in temporal visibility; do
  if [ "$store" = temporal ]; then export SQL_DATABASE=temporal; else export SQL_DATABASE=temporal_visibility; fi
  # Setup does not overwrite an existing version table; update applies only
  # migrations newer than the stored version. Never use overwrite/drop here.
  timeout 90 temporal-sql-tool setup-schema -v 0.0
  timeout 90 temporal-sql-tool update-schema -d "/etc/temporal/schema/postgresql/v12/$store/versioned"
done
