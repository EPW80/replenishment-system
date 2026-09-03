# 0009 — Client-supplied `idempotency_key` for SkipNext and Defer

**Status:** Accepted

## Context

ADR 0008 closed schedule creation's idempotency gap and named a narrower, related one
in its Consequences: `SkipNext` and `Defer` each resolve their target occurrence
implicitly — "whichever upcoming occurrence is next" — rather than by an identifier the
caller names. That resolution is not stable across a retry, because the first call's
own mutation is what changes which occurrence is next.

Concretely: a customer's client calls `POST /schedules/{id}/skip`, the request
succeeds, but the response is lost to a timeout. The client retries the identical
request. The first call already skipped occurrence 3, so occurrence 4 is now "next" —
the retry skips occurrence 4 as well. The customer asked to skip one shipment and two
disappeared. `Defer` has the same shape of failure: a retry can shift a second,
different occurrence, or shift the same occurrence a second time if it is still
soonest after the first shift.

`occurrences.idempotency_key` (spec §3) and `origin_order_id` (ADR 0008) do not apply
here. Both are keys tied to a real-world identifier the system already has — a
sequence number, a WooCommerce order ID. There is no equivalent for "a customer clicked
skip": no natural identifier names one button press as distinct from the next.

Pause, Resume, Cancel, and ChangeCadence do not have this problem. Each of them acts on
the schedule's own status rather than resolving a target occurrence, so a retry either
repeats a no-op precondition failure (pausing an already-paused schedule, canceling an
already-canceled one) or is a correct repeat of the same effect — safely, not merely by
accident, because `domain.TransitionError` already turns "wrong state to do this" into
a stable 409 regardless of how many times it's sent.

## Decision

**`SkipNext` and `Defer` each require a client-supplied `idempotency_key`.** The client
generates one per logical action (a UUID or any sufficiently unique token) and resends
the same value on retry. `ValidateIdempotencyKey` (`internal/domain/transitions.go`)
rejects it as required, not optional — empty is a bug in the caller, not a caller
opting out of the guard.

**Same key twice is a no-op that still returns 200,** mirroring "same key twice
produces one result, always" from spec §3 and ADR 0008. Before resolving a target
occurrence at all, the transition checks whether an event of that type already carries
this `(schedule_id, event_type, idempotency_key)`. If so, it returns the current
schedule state without touching an occurrence a second time.

**The check runs inside the row lock ADR 0007 already holds for every transition,**
not as a separate pre-check. Without that lock, two concurrent replays could both
observe "not done yet" before either writes, and the same duplicate-mutation bug this
ADR closes would reopen as a race instead. The check is safe to act on specifically
because nothing else can be appending a competing event for this schedule between the
read and the mutation that follows it.

**Storage: a nullable `idempotency_key` column on `schedule_events`, with a partial
unique index** on `(schedule_id, event_type, idempotency_key) WHERE idempotency_key IS
NOT NULL` (migration `00004_event_idempotency_key.sql`):

- Nullable, because system-authored events (materialize, sweep) and the other four
  transitions have no retry-ambiguity to guard against and so carry no key. Postgres
  does not constrain multiple `NULL`s under a `UNIQUE` index, so this needs no special
  handling to allow them.
- Scoped to `(schedule_id, event_type, idempotency_key)` rather than to the key alone,
  because two different schedules' customers could hand their own clients the same key
  value, and even on one schedule a skip and a defer are different actions that should
  not collide on a coincidentally-shared key.

**Rejected: naming the target occurrence's `sequence_no` instead of a generated key.**
This was considered because it reuses an identifier the system already has, the way
`origin_order_id` does. It was rejected because it only solves half the problem: it
would stop a retry from resolving a *different* occurrence, but a `Defer` retry naming
the same `sequence_no` twice would still shift that occurrence's date a second time —
there is no natural identifier for "this is the same defer request" the way there is
for "this is the same order." A generated key closes both failure modes uniformly for
both transitions.

## Consequences

- `POST /schedules/{id}/skip` and `POST /schedules/{id}/defer` now require
  `idempotency_key` in the request body. This is a breaking change to both request
  shapes for any existing caller; there is no compatibility mode, because an endpoint
  that silently accepts "no idempotency key" defeats the point, the same reasoning ADR
  0008 applied to schedule creation.
- `ScheduleEvent.IdempotencyKey` is `nil` for every event type except
  `occurrence.skipped` and `occurrence.deferred`. A reader of the event log should not
  expect this field populated elsewhere.
- The migration is additive and backward compatible: the column is nullable with no
  default, and the partial index only ever constrains rows that name a key. Rollback
  (`DROP INDEX`, `DROP COLUMN`) is safe — a pre-migration binary never read or wrote
  this column.
