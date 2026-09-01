# 0007 — Postmark over plain HTTP, and an outbox dispatcher off the event log

**Status:** Accepted

## Context

Phase 4 (spec §7) needed a way to send four transactional emails — schedule
created, and paused / resumed / canceled — that already have real trigger events
from Phase 3. Four decisions came up building it that aren't spelled out in the
spec and are worth recording.

## Decision 1 — Postmark client is hand-rolled over `net/http`, provisional

Spec §7: "same client pattern as PartnerOS." PartnerOS isn't reachable from this
environment — the same situation ADR 0002 (queue) and ADR 0003 (migrations/data
access) already documented. Postmark's send API is a single `POST
https://api.postmarkapp.com/email` with a server-token header and a small JSON
body, so this is implemented directly rather than importing an SDK: one more
supply-chain dependency for one HTTP call is a poor trade, the same reasoning
`docs/RECOMMENDED_ACTIONS.md` already applies to GitHub Actions.

**Provisional, like ADR 0002/0003:** if PartnerOS's actual Postmark client differs
meaningfully, `notify.Sender` is the seam — swapping the implementation touches
`internal/notify/sender.go` only.

## Decision 2 — the dispatcher is a transactional outbox off `schedule_events`, not a new delivery path

`internal/schedule/service.go`'s transitions commit their state change and their
audit event together, inside `InTx`. Sending email synchronously inside that same
transaction would mean a slow or down Postmark holds a database transaction open
for network latency, and — worse — a customer's pause request could fail or roll
back for a reason that has nothing to do with the pause itself.

Instead, `internal/notify.Dispatcher` polls `schedule_events` (via
`store.Repository.UnnotifiedEvents`) for events with no matching row in a new
`notification_log` table, and records one there after a successful send. This is
not new machinery — it's the same projection style
`internal/readmodel` already uses on the same log (spec §8's CQRS seam, spec §3).
`cmd/notify` runs it on a schedule, the same operational shape as
`cmd/materialize`.

## Decision 3 — delivery is at-least-once, deliberately

The send happens before `RecordNotification` is called. A crash in between could
in theory resend one email. This is accepted rather than engineered around,
because the two failure modes are not comparable: a duplicate occurrence
(spec §3's idempotency key) is a duplicate *charge*, which this system cannot
tolerate under any circumstance. A duplicate confirmation email is a minor
annoyance. Building exactly-once delivery here — a distributed transaction or a
claim/lease pattern — would spend real complexity on a risk that doesn't justify
it.

## Decision 4 — no customer-portal links in templates

"How to change it" (created) and "reversal path" (paused/resumed/canceled) are
spec §7's own content requirements, but Phase 5 (the customer portal) doesn't
exist yet, so there is nowhere for such a link to point. Every template says
"contact us" with a support address instead of inventing a URL or leaving the
requirement unaddressed. Revisit once Phase 5 ships a real portal URL to link to.

## Consequences

- `customer_email` was added to `schedules` (migration `00003`) as part of this
  work, not deferred like ADR 0006's gaps — Phase 4 cannot function at all
  without an address to send to, unlike ADR 0006's price and acquisition-source
  gaps, which had no consumer inside Phase 7 itself.
- The "next order date" in templates is computed directly from the schedule's
  current anchor (`domain.OccurrenceDate(anchor, interval, 1)`), not read from
  `next_run_date`. A freshly created schedule has no materialized occurrence yet
  — that's `cmd/materialize`'s job, run on its own schedule — so relying on
  `next_run_date` would make the correctness of a notification depend on which
  job happened to run first. Computing it directly removes that dependency
  entirely; it also happens to be correct immediately after a resume, since resume
  always re-anchors to today first.
- Pre-billing notice, order-placed, and the payment-failure dunning ladder are not
  built here. Dunning reacts to payment attempts that only Phase 2 can produce,
  despite spec §10 nominally filing "dunning ladder" under Phase 4's heading.
