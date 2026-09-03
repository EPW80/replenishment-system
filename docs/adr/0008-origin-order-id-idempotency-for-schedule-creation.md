# 0008 — `origin_order_id` as schedule creation's idempotency key

**Status:** Accepted

## Context

`POST /schedules` had no idempotency protection. Nothing tied a schedule to the
WooCommerce checkout that created it, and there was no client-supplied key or unique
constraint of any kind on the insert. A retried request from the WP mu-plugin — a
timeout, a duplicate webhook delivery, a redeploy mid-request — created a second,
fully independent, active schedule for the same checkout.

Demonstrated rather than assumed: two byte-identical `POST /schedules` calls against a
running instance returned two distinct schedule rows, each active, each materializing
its own occurrence horizon.

`occurrences.idempotency_key` (spec §3) is documented as "the whole safety story for
order creation," but it only guards a duplicate *occurrence* within one schedule. It
has no opinion on the schedule itself being duplicated. Once Phase 2 (WooCommerce order
execution) ships, a duplicated schedule is not a one-time double charge — it is two
independent recurring-order streams billing the same customer indefinitely, each with
its own idempotency-protected occurrences that are individually "correct."

## Decision

**`origin_order_id`, the WooCommerce order ID from the checkout that established the
subscription, is schedule creation's idempotency key** — the same role
`occurrences.idempotency_key` plays for a single occurrence. It is a real identifier
the mu-plugin already has from the checkout it just processed, not an invented key.
`UNIQUE`, `NOT NULL` on `schedules`.

**Same key twice returns the existing schedule, `200 OK`, rather than erroring or
creating a second one.** `CreateSchedule` maps the unique-constraint violation to
`ErrDuplicateSchedule`; the handler catches it, looks up the schedule by
`origin_order_id`, and returns it — mirroring "same key twice produces one occurrence,
always" from spec §3, at the schedule level instead of the occurrence level.

**The column ships with a non-constant `DEFAULT` (`gen_random_uuid()::text`) rather
than as nullable-then-backfilled.** This does three things in one migration:

- Backfills any schedule that existed before this migration with a unique synthetic
  value, so the `NOT NULL UNIQUE` constraint can be added directly without a separate
  backfill step or a window where the column is nullable.
- Keeps the migration backward compatible: a pre-migration binary's `INSERT` statement
  does not name this column at all, and the `DEFAULT` fills it in rather than the
  insert failing. Application code always supplies a real value going forward, so the
  default is a rollback safety net, not a path any current code relies on.
- Keeps rollback safe: redeploying the previous binary against the new schema keeps
  working — it just does not get the idempotency guarantee it did not have anyway,
  which is the pre-existing (bug) behavior, not a regression from the rollback.

**Unscoped lookup.** `GetScheduleByOriginOrderID` takes no `Scope` — it backs the
retry path off `POST /schedules`, which runs on the service credential rather than a
customer token, the same trust level as schedule creation itself. `origin_order_id` is
globally unique, so there is nothing to scope by.

## Consequences

- `POST /schedules` now requires `origin_order_id` in the request body. This is a
  breaking change to the request shape for any caller — currently only the WP
  mu-plugin per spec §4 — that does not send it; there is no compatibility mode,
  because a create endpoint that will silently accept "no idempotency key" defeats the
  point.
- The response now also carries `origin_order_id`, unlike `payment_token_ref` (kept
  write-only) — it is not sensitive, and surfacing it lets a caller confirm which
  checkout a schedule resolved to after a retry.
- A schedule can no longer be created without knowing which checkout it came from.
  That is the intended constraint, not a side effect: the whole point is that
  "checkout" and "schedule" are now provably 1:1.
- The related, lower-severity gap this review also found — `SkipNext`/`Defer` picking
  "whichever occurrence is next" with no per-occurrence idempotency key, so a retried
  skip/defer can act on two different occurrences instead of no-op'ing — is out of
  scope here and tracked separately. It needs a different shape of fix (a
  client-supplied key scoped to one occurrence, not a schedule-level identifier) and
  bundling it here would violate "one PR, one concern."
