# 0002 — `river` for the durable queue, behind a `Queue` interface

**Status:** Accepted, provisional

## Context

Occurrence execution needs a durable queue: a job that survives process restart, does
not lose work mid-run, and does not double-execute. Spec §4 is explicit — "reuse
whatever PartnerOS settled on rather than introducing a second queue library."

**PartnerOS was not reachable from the environment where this decision was made.** The
choice below is therefore provisional, and the interface matters more than the pick.

## Decision

Use `river` (Postgres-backed job queue for Go), accessed only through a `Queue`
interface defined in `internal/materialize`.

`river` because it stores jobs in the same Postgres instance the service already
depends on: no second piece of infrastructure, and job enqueue can share a transaction
with the state change that caused it. `asynq` (the alternative named in spec §4) would
add Redis as a second stateful dependency.

## Consequences

- **This decision is expected to be revisited** the moment PartnerOS's actual choice
  is known. If PartnerOS uses `asynq`, switch — a second queue library across two
  sibling services is exactly what spec §4 warns against.
- Because callers depend only on the `Queue` interface, switching is an adapter swap,
  not a rewrite. Spec §12 treats anything else as a design bug.
- Phase 1 ships an in-process implementation of `Queue` and no queue library at all.
  Nothing in Phase 1 places an order, so durability is not yet load-bearing, and the
  dependency can be deferred until Phase 2 where it is.
