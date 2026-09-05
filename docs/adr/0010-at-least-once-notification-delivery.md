# 0010 — At-least-once notification delivery

**Status:** Accepted

## Context

Phase 4 (issue #4) added the four spec §7 transactional sends that Phase 3's state
machine already produces real trigger events for: schedule created, and paused /
resumed / canceled. Something had to decide how hard the dispatcher tries not to send
the same email twice.

The obvious instinct is to reuse the guarantee the rest of the system already has. It
is a strong one: `occurrences.idempotency_key` is `UNIQUE` and is the whole safety
story for order creation (spec §3), and ADR 0009 went as far as requiring a
client-supplied key on `SkipNext` and `Defer` rather than let a retry resolve a
different occurrence. Both records treat a duplicate as unacceptable.

They are unacceptable there for a specific reason, and the reason does not carry over.
A duplicate occurrence is a second charge. A duplicate skip removes a shipment the
customer never asked to lose. In both cases the duplicate *corrupts state* — it leaves
the system holding something false about the customer's schedule, and no later pass can
tell that it happened. A duplicate "your schedule is paused" email is cosmetic: mildly
annoying, self-evidently redundant to the person reading it, and it changes nothing in
the database.

That asymmetry is worth spending, because exactly-once here is not cheap. The send is a
network call to Postmark, and the record of it is a row in our own database. Making
those two atomic means either holding a database transaction open across the Postmark
call, or building a two-phase protocol over an API that offers no transactional handle
to build one on. The first is the worse option and is the one worth naming explicitly:
a network call has no business holding a row lock, and under a batch of a hundred sends
it turns one slow Postmark response into lock contention across the table.

The remaining question is where the outbox lives. Populating a queue at write time from
the four emit sites (`httpapi.Create`, and `schedule.Service`'s `Pause`, `Resume`,
`Cancel`) would add a second write path alongside `schedule_events`, and two write
paths describing the same facts drift.

## Decision

**Delivery is at-least-once. A duplicate email is possible and accepted.** The dispatcher
does not attempt exactly-once, and no constraint in the schema pretends to provide it.

**The gap that makes a duplicate possible is deliberate and known.**
`MarkNotificationSent` is issued as its own statement after the Postmark call returns,
never inside the transaction that claimed the row (`internal/store/store.go`). A crash
between "Postmark accepted the message" and "we recorded that it did" leaves the row
`pending`, and a later run reclaims and resends it. That window is the price of not
holding a lock across a network call, and it buys a duplicate of the cheapest possible
kind.

**The outbox is derived, not dual-written.** `ClaimNotifiableEvents` reads
`schedule_events` and `LEFT JOIN`s `notification_log`, taking rows where no log row
exists yet. A row in `notification_log` means "this event has been attempted," nothing
more. Consequences: the outbox cannot drift from the event log it describes, because it
*is* the event log plus an attempt record; and a new notifiable event type is added by
extending the dispatcher's query, not by touching any emit site.

**`UNIQUE (schedule_event_id)` on `notification_log` is bookkeeping, not safety.** Each
event gets at most one outbox row so the claim's `ON CONFLICT ... DO UPDATE` can
increment an attempt counter in place. It is emphatically not playing the role
`occurrences.idempotency_key` plays — read as a safety constraint it would misdescribe
the guarantee this ADR establishes.

**A claim marks its own attempt in the same statement.** The claim is one query:
`FOR UPDATE OF e SKIP LOCKED` over the candidates, then an `INSERT ... ON CONFLICT DO
UPDATE` that stamps `attempts` and `last_attempt_at`, returning what it claimed. Two
overlapping `cmd/notify` runs therefore cannot both claim the same event — the second
skips locked rows rather than duplicating the first run's work.

**A row stuck at `pending` past `visibilityTimeout` (15 minutes) is reclaimable.**
`cmd/notify` is a short, infrequent one-shot process rather than a long-running worker,
so a claim that outlives fifteen minutes was almost certainly abandoned by a crash, not
held by a slow send. Reclaiming beats the alternative, which is a notification lost
forever because the process that claimed it died. This is what makes the at-least-once
gap above recoverable rather than merely tolerated.

**`maxAttempts = 5`, then the row is `failed` and never reclaimed.** Without a cap, one
permanently bad address retries on every run forever and crowds out newer sends. Note
that a Postmark rejection is not a Go error in the dispatcher — it is a normal outcome
recorded through `MarkNotificationFailed`, because a batch of a hundred events must not
stop because one address bounced.

**The schedule is read fresh at send time**, not carried on the event. A customer who
corrects their `customer_email` after the event was recorded receives at the address
they have now, rather than at whichever one was current when they clicked pause. A
schedule with no email at all — created before Phase 4, or by a caller that never sent
one — is marked resolved rather than retried, since there is nothing to send and it is
not a failure.

## Consequences

- **A customer can receive the same confirmation email twice.** Support should treat a
  reported duplicate as expected behavior, not as a bug to investigate. The realistic
  trigger is a crash or a redeploy landing inside the window between a successful
  Postmark call and its acknowledgement.
- **The delivery guarantee is not uniform across the system, on purpose.** Occurrences
  and skip/defer are exactly-once (ADRs 0008, 0009); notifications are at-least-once.
  Anyone reading `notification_log`'s unique constraint as the same kind of guard the
  occurrence key provides will misread it — hence this record.
- **A permanently failing address goes quiet after five attempts.** The row is `failed`
  with `last_error` populated, and nothing raises an alarm about it: there is no
  failure-queue view until Phase 6. Until then, discovering a stuck notification means
  querying `notification_log` directly.
- **Notifications can be delayed by up to the visibility timeout** in the crash case,
  since a claimed-but-unresolved row is not reconsidered for fifteen minutes. That is
  acceptable for pause/resume/cancel confirmations. It would not be acceptable for the
  T-72h pre-billing notice, which is a legal and UX requirement (spec §5) — when that
  send arrives with Phase 2, this timing needs revisiting rather than inheriting.
- **Rendering happens per event rather than per batch**, so a template failure fails one
  notification instead of the run.
