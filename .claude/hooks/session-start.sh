#!/bin/bash
#
# SessionStart hook for Claude Code on the web.
#
# `make db-up` starts Postgres through Docker Compose, and a web session container has
# no Docker daemon -- so without this hook the service cannot start and, worse, the
# integration suite disappears silently: internal/testsupport.DB skips when
# DATABASE_URL is unset, and `go test` prints "ok" for a package in which nothing ran.
#
# The container does ship a Postgres 16 cluster, just stopped. This starts it, creates
# the same role and database docker-compose.yml defines, and exports DATABASE_URL so
# the Makefile default (Makefile:15) resolves to a database that actually exists.
#
# Local development is untouched: this exits immediately outside a web session, where
# `make db-up` is the documented path (README.md) and CI has its own service container
# (.github/workflows/test.yml).
set -euo pipefail

# Synchronous, not async: `make test` and `make run` both need the database to exist
# before the agent's first command, and an async hook would race them.

if [ "${CLAUDE_CODE_REMOTE:-}" != "true" ]; then
	exit 0
fi

log() { echo "session-start: $*"; }

PG_USER=cadenceos
PG_PASSWORD=cadenceos
PG_DB=cadenceos_dev
DATABASE_URL="postgres://${PG_USER}:${PG_PASSWORD}@localhost:5432/${PG_DB}?sslmode=disable"

# --- Postgres ------------------------------------------------------------------
# Every step below is idempotent: the hook re-runs on resume and on clear, and a
# second run must not fail on a cluster that is already up or a role that exists.

if ! command -v pg_ctlcluster >/dev/null 2>&1; then
	log "no Postgres installation found; skipping database setup"
	log "the DB integration tests will skip -- see .github/workflows/test.yml for the CI path"
else
	if pg_isready --quiet 2>/dev/null; then
		log "cluster already running"
	else
		log "starting the Postgres 16 cluster"
		pg_ctlcluster 16 main start

		# pg_ctlcluster returns before the postmaster accepts connections.
		for _ in $(seq 1 30); do
			pg_isready --quiet 2>/dev/null && break
			sleep 1
		done
		pg_isready --quiet || { log "cluster did not become ready"; exit 1; }
	fi

	psql_super() { su postgres -c "psql --no-psqlrc --quiet -tAc \"$1\""; }

	if [ "$(psql_super "SELECT 1 FROM pg_roles WHERE rolname = '${PG_USER}'")" = "1" ]; then
		log "role ${PG_USER} exists"
	else
		log "creating role ${PG_USER}"
		psql_super "CREATE ROLE ${PG_USER} LOGIN PASSWORD '${PG_PASSWORD}'"
	fi

	if [ "$(psql_super "SELECT 1 FROM pg_database WHERE datname = '${PG_DB}'")" = "1" ]; then
		log "database ${PG_DB} exists"
	else
		log "creating database ${PG_DB}"
		su postgres -c "createdb -O ${PG_USER} ${PG_DB}"
	fi
fi

# --- Go modules ----------------------------------------------------------------
# Warms the module cache, which the container image caches after the hook completes.
# `go mod download` rather than anything stricter: go.sum verification is `make deps`'
# job and belongs in CI, not in a session-startup path.

if [ -n "${CLAUDE_PROJECT_DIR:-}" ]; then
	cd "$CLAUDE_PROJECT_DIR"
fi

log "downloading Go modules"
go mod download

# --- Session environment -------------------------------------------------------
# DATABASE_URL only. The auth secrets cmd/cadenceos requires are deliberately not set
# here: they are per-developer values generated with `openssl rand`, and a hook that
# invented them would put a known-value credential in every session (CLAUDE.md rule 7).
# `make run` still needs them from .env -- see .env.example.

if [ -n "${CLAUDE_ENV_FILE:-}" ]; then
	echo "export DATABASE_URL=\"${DATABASE_URL}\"" >> "$CLAUDE_ENV_FILE"
	log "exported DATABASE_URL"
fi

log "ready -- run 'make migrate' to apply migrations"
