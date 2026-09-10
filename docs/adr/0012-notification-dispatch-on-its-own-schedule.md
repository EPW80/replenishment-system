# 0012 — Notification dispatch runs on its own schedule

**Status:** Accepted

## Context

`cmd/notify` sends the four Phase 4 transactional emails (spec §7): schedule created,
paused, resumed and canceled. It is finished, tested, and idempotent under the
at-least-once contract in [ADR 0010](0010-at-least-once-notification-delivery.md).

Nothing runs it. `scripts/nightly.sh` invokes `sweep` and `materialize` only,
`docs/SCHEDULED_JOBS.md` did not mention notify, and its only invocation path was
`make notify` by hand. In staging and production every confirmation the service has
ever queued is sitting unsent in `notification_log`.

This is precisely the failure [ADR 0011](0011-coolify-scheduled-tasks-for-nightly-jobs.md)
was written about — its Context observes of materialize and sweep that both were
"documented in their own file headers and in `CLAUDE.md` as intended to run nightly.
Nothing ran them." `cmd/notify` is the third such job. It was not covered when 0011
shipped, and its own header still claimed it was "intended to run nightly, alongside
materialize and sweep."

That header is the crux. It reads as though the fix is one line in `nightly.sh`. It is
not, for two independent reasons.

**Spec §7 requires immediate delivery.** The Transactional sends table carries a Timing
column, and both rows this job serves say `immediate`:

| Trigger | Timing |
|---|---|
| Schedule created | immediate |
| Paused / resumed / canceled | immediate |

A nightly pass means a customer who pauses at 10:05 UTC learns the next morning that
their orders have stopped. No reading of "immediate" survives a 24-hour worst case, and
these are specifically the reversal-path emails — the ones that tell someone what
changed and how to undo it. They are the least tolerable to sit on.

**It would falsify `nightly.sh`'s environment invariant.** That script guards on
`DATABASE_URL` and states in its own comment: "DATABASE_URL is the only variable these
jobs need. Neither authenticates a caller, so neither takes `PORTAL_JWT_SECRET` or
`SERVICE_API_KEY` — do not hand a scheduled job credentials it never presents." ADR
0011 records the same rule. `cmd/notify` needs `POSTMARK_API_KEY`,
`NOTIFICATION_FROM_ADDRESS` and `NOTIFICATION_SUPPORT_CONTACT`, so adding it would force
that script — and therefore `sweep` and `materialize` — to carry credentials neither of
them presents.

Worth stating precisely, because it is easy to over-apply: the rule is about
*presenting* credentials, not about secrecy. Giving notify its Postmark variables is
correct; it presents them to Postmark. Giving them to the nightly pair is not.

## Decision

**Notification dispatch runs as its own Coolify scheduled task, every five minutes,
separate from the nightly pair.**

Command: `notify`. No wrapper script.

`Dockerfile` builds `./cmd/...` — every command, not just the service — and copies the
output to `/usr/local/bin/`, so the binary is already on `PATH` in the deployed image.
(ADR 0011 noted that no Dockerfile was checked in when it was written and that
`nightly.sh` resolved binaries defensively as a result. One exists now.) A wrapper would
add nothing: `nightly.sh` earns its place by sequencing two jobs and continuing past a
failure in the first, and a single job has neither problem.

**Five minutes.** Well inside what anyone experiences as immediate for email, and modest
enough on the database. One minute would buy nothing a customer could perceive.

**Overlapping runs need no coordination.** `ClaimNotifiableEvents` claims in a single
statement whose CTE uses `FOR UPDATE ... SKIP LOCKED`, and whose candidate predicate
excludes any row whose `last_attempt_at` falls inside the visibility timeout. A run
still working when the next tick starts has its in-flight rows skipped rather than
re-sent. `cmd/notify` carries a 10-minute internal timeout, so a slow run can overlap
the following two ticks without incident.

**`notify.visibilityTimeout` stays at 15 minutes.** It decides when a *crashed* run's
claim is treated as abandoned, which is independent of how often the job starts.
Shortening it to match the cadence would make a merely slow run look abandoned and
invite the duplicate that ADR 0010 accepts but does not seek.

### Rejected: add `notify` to `scripts/nightly.sh`

The obvious one-line fix, and what `cmd/notify`'s own header implied. Rejected on both
counts above: it cannot deliver `immediate`, and it would put Postmark credentials in
the environment of two jobs that never present them. It would also make `make nightly`
— documented as running the passes "exactly as the scheduler runs them" — stop matching
what the scheduler runs, unless a third job were added to that correspondence too.

### Rejected: send inline from the transition handlers

Emitting the email in the same request that pauses a schedule would be genuinely
immediate and would need no scheduler at all. Rejected because it puts a third-party
HTTP call inside a transaction that holds a row lock
([ADR 0007](0007-row-locking-for-transitions.md)): a slow Postmark response would hold
the lock, and a failed one would either roll back a transition the customer already
completed or be swallowed. The outbox in `notification_log` exists specifically to keep
delivery off that path, and ADR 0010 already settled that trade.

## Consequences

- **A second cron expression lives outside the repository.** ADR 0011 named this as the
  real, not-fully-mitigable cost of Coolify tasks, and this decision doubles it: there
  are now two tasks that can drift from `docs/SCHEDULED_JOBS.md` or fail to exist at
  all, with nothing failing a build. The mitigation is unchanged — the doc carries the
  exact configuration, and checking it belongs in the deploy checklists.
- **Alerting is still absent, and now matters twice.** Coolify records each task's exit
  status; nothing pages on it. A dispatch task failing for a week is as quiet as one
  never created. `docs/SCHEDULED_JOBS.md` carries an operator query for unsent
  notifications as the manual stand-in, in the same spirit as the horizon and paused
  queries already there.
- **Worst-case delivery latency is five minutes plus send time**, not zero. This is a
  polling outbox, so "immediate" is approximated rather than achieved. If a stricter
  bound is ever needed, the fix is a queue (ADR 0002's `river`, still unadopted), not a
  shorter cron.
- **Turning the task on starts real customer email in an environment where none has
  ever sent.** The backlog in `notification_log` will dispatch on the first run. On a
  database with a long accumulated history that first batch is capped at
  `notify.batchSize` (200) per run and drains over subsequent ticks, but it is worth
  knowing before enabling it in production rather than after.
- The task is per-deployment configuration, not architecture. A second brand on
  different hosting schedules the same binary however that platform prefers, without
  touching the service.
