# 0004 — Compute `next_run_date` anchor-relative, never incrementally

**Status:** Accepted

## Context

Every schedule needs a next date. The obvious implementation is
`next_run_date = last_run_date + interval_days`, updated after each shipment.

That is wrong in a way that is invisible until it has been running for months.

## Decision

`next_run_date` is **always** recomputed as `anchor_date + (n × interval_days)`, where
`n` is the occurrence sequence number and `anchor_date` is the schedule's origin.
Never `last_run + interval`. This is spec §3, and it is restated in `CLAUDE.md`.

The math lives in `internal/domain`, is pure (no I/O), and operates on **calendar
dates in the customer's IANA timezone**, not UTC instants.

## Consequences

- **Drift does not accumulate.** Incremental addition compounds every skip, deferral,
  and retry into a permanent slide; anchor-relative computation cannot drift because
  every date derives from the same origin.
- `defer` shifts an occurrence's `scheduled_for` **without** shifting the anchor
  (spec §6). A customer who pushes one shipment a week out returns to their normal
  rhythm afterward. This behaviour is only expressible because the anchor is separate
  from the occurrence date.
- `resume` and `change_cadence` re-anchor deliberately and explicitly (spec §6), which
  is a visible decision in the state machine rather than an emergent side effect.
- Date arithmetic must be done in date space, not by adding 24-hour durations. Adding
  `24h × n` to a timestamp crosses a DST boundary and silently produces an off-by-one
  shipment date. Priority test coverage per spec §10: DST boundaries, leap days, and
  month-end.
