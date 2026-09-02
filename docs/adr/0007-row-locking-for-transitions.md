# 0007 — Serialize transitions with a row lock, not optimistic retry

**Status:** Accepted

## Context

Every spec §6 transition read and validated the schedule outside the transaction that
then mutated it:

```go
s, err := svc.load(ctx, scheduleID, domain.ActionPause, caller.Scope)  // read + validate
...
err = svc.repo.InTx(ctx, func(tx store.Repository) error { ... })      // mutate
```

Nothing held the row across those steps and `UpdateScheduleStatus` carried no status
precondition, so two concurrent callers could each validate against a state the other
was changing and both write. Demonstrated rather than assumed: against that code, eight
concurrent cancels produced **two** winners and two `schedule_canceled` events, and
three concurrent skips consumed only **two** occurrences — two callers had chosen the
same one, so a customer asking to skip three shipments would have had two disappear.

ADR 0006 recorded this as the reason its authorization gate was "a check rather than a
lock". This is the decision that closes it.

The nightly materializer had the same shape at a different scale: `RunAll` listed
active schedules and then planned against that snapshot. The job runs long enough to
walk every schedule, which is ample time for a customer to pause one, and planning from
the snapshot leaves planned occurrences on a paused schedule — precisely the state
`Pause` exists to clear.

## Decision

**One transaction per transition, opened before the decision is made.** Lock the
schedule row with `SELECT … FOR UPDATE`, check the precondition against the state the
lock guarantees is current, apply the change and its audit event, and read back the
result — all inside it. `internal/schedule`'s `transition` helper is the only place
this sequence is written, so a new action cannot accidentally adopt a different one.

**Pessimistic locking rather than optimistic retry.** A version column with a
compare-and-set and a retry loop would also be correct, and would hold up better under
contention spread across many rows. It was not chosen because contention here is
per-schedule and rare — one customer, occasionally double-clicking, or a portal racing
its own retry — while a retry loop would need every transition to be safe to run twice
against partially-applied state, which is a much larger property to keep true as
actions are added. Blocking briefly on a row nobody else usually wants is the cheaper
guarantee.

**The loser gets 409, through the existing precondition path.** Under `READ COMMITTED`
a blocked `SELECT … FOR UPDATE` re-reads the committed row once the winner commits, so
the second caller validates against the *new* status and fails as a
`domain.TransitionError` — already mapped to 409. No new error type, and no "conflict"
concept the domain would otherwise have to carry.

**The locking read refuses to run outside a transaction** (`store.ErrNoTransaction`).
A `FOR UPDATE` in its own implicit transaction releases the lock as the statement
returns: it would read as locking while guaranteeing nothing, and a future caller could
adopt that false assurance and drop the transaction around a transition.

**`RunAll` takes a transaction per schedule**, locking and re-reading before planning,
one row at a time so a customer's transition waits on at most the single schedule the
job currently holds rather than on the whole run.

## Consequences

- Transitions on the same schedule serialize. Different schedules do not contend, and
  the locks are held only for the duration of one transition, so throughput is
  unaffected in the ordinary case.
- Every transition now locks the schedule row **first**, before touching occurrences.
  That consistent order is what keeps two transitions from deadlocking; a future action
  that takes locks in another order would reintroduce that risk.
- The authorization scope from ADR 0006 now applies to a locked read, so its gate is a
  lock rather than a check. The residual gap that ADR recorded is closed.
- Input validation that does not depend on the schedule (defer days, interval,
  cancellation reason) happens before the transaction opens, so a request that cannot
  succeed does not take a lock others are waiting on.
- A caller blocked on a locked row waits rather than failing fast. Postgres will hold
  it there indefinitely by default; the request's context deadline is what bounds it.
  If contention ever becomes real rather than incidental, `lock_timeout` is the knob,
  and that is the point to revisit optimistic locking instead.
