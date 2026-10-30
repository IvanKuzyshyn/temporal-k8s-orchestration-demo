#!/usr/bin/env bash
set -euo pipefail
# This runs only when PostgreSQL initializes a fresh volume. Schema changes are
# owned by the separate matching-version schema service, not by this init hook.
psql --username "$POSTGRES_USER" --dbname postgres -v ON_ERROR_STOP=1 <<'SQL'
CREATE DATABASE temporal_visibility OWNER temporal;
SQL
