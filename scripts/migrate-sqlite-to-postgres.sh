#!/usr/bin/env bash
# Migrate live SQLite data into PostgreSQL for TruERP.
#
# Usage (from backend/):
#   cp .env.example .env   # set DATABASE_URL for Postgres
#   ./scripts/migrate-sqlite-to-postgres.sh
#
# Environment:
#   SQLITE_PATH      Source SQLite file (default: data/truerp.db or DATABASE_PATH)
#   DATABASE_URL     Target PostgreSQL connection string (required)
#   SKIP_BACKUP      Set to 1 to skip SQLite backup
#   BATCH_SIZE       Insert batch size (default: 500)
#
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

SQLITE_PATH="${SQLITE_PATH:-${DATABASE_PATH:-data/truerp.db}}"
if [[ ! -f "${SQLITE_PATH}" && -f data/billbook.db ]]; then
  SQLITE_PATH="data/billbook.db"
fi
DATABASE_URL="${DATABASE_URL:-}"
BATCH_SIZE="${BATCH_SIZE:-500}"
STAMP="$(date +%Y%m%d-%H%M%S)"

if [[ ! -f "${SQLITE_PATH}" ]]; then
  echo "error: SQLite database not found at ${SQLITE_PATH}" >&2
  exit 1
fi

if [[ -z "${DATABASE_URL}" ]]; then
  echo "error: DATABASE_URL is required for PostgreSQL target" >&2
  exit 1
fi

echo "==> TruERP SQLite → PostgreSQL migration"
echo "    Source: ${SQLITE_PATH}"
echo "    Target: ${DATABASE_URL%%@*}@***"

if [[ "${SKIP_BACKUP:-0}" != "1" ]]; then
  BACKUP="${SQLITE_PATH}.backup-${STAMP}"
  echo "==> Backing up SQLite to ${BACKUP}"
  cp -a "${SQLITE_PATH}" "${BACKUP}"
  if [[ -d uploads ]]; then
    UPLOADS_BACKUP="uploads.backup-${STAMP}"
    echo "==> Backing up uploads/ to ${UPLOADS_BACKUP}"
    cp -a uploads "${UPLOADS_BACKUP}"
  fi
else
  echo "==> Skipping backup (SKIP_BACKUP=1)"
fi

echo ""
echo "==> Step 1/4: Dry run (preview row counts)"
go run ./cmd/migrate-sqlite-to-postgres \
  -sqlite "${SQLITE_PATH}" \
  -postgres "${DATABASE_URL}" \
  -copy \
  -dry-run

echo ""
read -r -p "Proceed with migration? This will TRUNCATE Postgres and copy all data. [y/N] " confirm
if [[ "${confirm}" != "y" && "${confirm}" != "Y" ]]; then
  echo "Aborted."
  exit 0
fi

echo ""
echo "==> Step 2/4: Prepare PostgreSQL schema"
go run ./cmd/migrate-sqlite-to-postgres \
  -sqlite "${SQLITE_PATH}" \
  -postgres "${DATABASE_URL}" \
  -prepare-schema

echo ""
echo "==> Step 3/4: Copy data (truncate + load)"
go run ./cmd/migrate-sqlite-to-postgres \
  -sqlite "${SQLITE_PATH}" \
  -postgres "${DATABASE_URL}" \
  -copy \
  -truncate \
  -batch-size "${BATCH_SIZE}"

echo ""
echo "==> Step 4/4: Validate"
go run ./cmd/migrate-sqlite-to-postgres \
  -sqlite "${SQLITE_PATH}" \
  -postgres "${DATABASE_URL}" \
  -validate

echo ""
echo "==> Migration complete"
echo ""
echo "Next steps:"
echo "  1. Set DATABASE_URL in .env (unset DATABASE_PATH)"
echo "  2. Copy uploads/ to the Postgres server if deploying remotely"
echo "  3. Restart the backend and smoke-test login, invoices, and reports"
echo "  4. Keep SQLite backup at ${SQLITE_PATH}.backup-${STAMP} for rollback"
