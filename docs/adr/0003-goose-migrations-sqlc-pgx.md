# 0003 — `goose` for migrations, `sqlc` + `pgx` for data access

**Status:** Accepted, provisional

## Context

The service needs schema migrations that run in a gated deploy step and a data-access
layer. As with ADR 0002, spec §4 prefers whatever PartnerOS uses; PartnerOS was not
reachable.

## Decision

- **`goose`** for migrations: plain SQL files, explicit up/down, no ORM-driven
  schema generation. Migrations are reviewable as SQL, which matters because
  `CLAUDE.md` requires every migration PR to state backward-compatibility and rollback
  safety — a reviewer has to be able to read what the migration does.
- **`sqlc` + `pgx`** for data access: queries are written as SQL and compiled to typed
  Go, so the compiler catches a column rename. No ORM.

Both sit behind the `Repository` interface in `internal/store`.

## Consequences

- Hand-written SQL is more verbose than an ORM. Accepted: the schema is small and
  fixed by spec §3, and explicit SQL is what makes the migration review gate real.
- `sqlc` generates code from the migrations, which gives the compliance guard
  (ADR 0005) a second surface to scan — generated model fields as well as raw SQL.
- Down-migrations exist but are not the rollback story. `rollback_safe` in the release
  record is about redeploying the previous *application* version, which is a different
  question from whether the schema can be reversed. See `docs/RELEASE_METADATA.md`.
- Provisional for the same reason as ADR 0002 — revisit against PartnerOS.
