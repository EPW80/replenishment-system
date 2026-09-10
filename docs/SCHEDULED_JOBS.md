# Scheduled jobs

Three jobs run on a schedule, as **two** Coolify tasks with different cadences and
different environments.

| Job | Task | Cadence | What it does | Spec |
| --- | --- | --- | --- | --- |
| `sweep` | nightly | daily | Ends timed pauses that have come due, re-anchoring the schedule to today | §6 resume |
| `materialize` | nightly | daily | Tops the planned-occurrence horizon back up for every active schedule | §5 step 1 |
| `notify` | notification dispatch | every 5 min | Sends the outstanding transactional email sitting in the outbox | §7 |

The two nightly passes move schedules forward with no customer action, and run from one
script, `scripts/nightly.sh`. `notify` is deliberately **not** in that script: spec §7
requires its emails to be immediate, and it needs Postmark credentials the other two
never present. [ADR 0012](adr/0012-notification-dispatch-on-its-own-schedule.md) records
that decision and the alternatives rejected.

The reasoning for using Coolify tasks at all — and the two options rejected there — is
[ADR 0011](adr/0011-coolify-scheduled-tasks-for-nightly-jobs.md).

None of the three places an order. Arm, Execute and Reconcile (spec §5 steps 2–4) are
Phase 2.

## Why this matters

The failure mode is silent. Nothing breaks on the first night the scheduler is missing.
The horizon drains one occurrence at a time as the portal consumes it, and customers
who paused until a date are never resumed. The symptom — an empty upcoming queue, a
schedule stuck paused past its date — shows up weeks after the cause.

So the check that matters is not "did tonight's run succeed" but "has a run happened at
all recently." See [Verifying](#verifying) below.

Dispatch fails differently but just as quietly. Nothing errors when `notify` is not
running: transitions still succeed, events are still appended, and the outbox simply
accumulates. The customer sees a schedule that paused without ever telling them, which
is the one outcome spec §7 is written to prevent — it calls these sends the confirmation
*and the reversal path*. The symptom surfaces as a support conversation, not as a log
line, so it also needs a query rather than a green run.

## What the nightly script does

`scripts/nightly.sh` runs `sweep`, then `materialize`, and **runs both even if the
first fails**, exiting non-zero if either did.

The order is for legibility rather than correctness: the two jobs are independent,
because `Service.Resume` re-materializes inside its own transaction, so a failed sweep
cannot leave a resumed schedule without occurrences. Running sweep first means
materialize's `schedules_considered` count is a true snapshot of the active set,
including anything resumed that night.

Running both regardless is the part that carries weight. Materialize is the job whose
absence drains the horizon; skipping it because sweep failed would convert one
recoverable error into exactly the silent decay above.

Both jobs are idempotent. Running the script twice in a night creates nothing the
second time — `occurrences.idempotency_key` is `UNIQUE` and is the whole safety story
for order creation, so a duplicate run, a retry, or an overlapping invocation cannot
produce a second charge.

## Environment

The two tasks need different variables, and the difference is the reason they are two
tasks.

**Nightly pair** — `DATABASE_URL`, and nothing else.

**Notification dispatch** — `DATABASE_URL` plus `POSTMARK_API_KEY`,
`NOTIFICATION_FROM_ADDRESS` and `NOTIFICATION_SUPPORT_CONTACT`. `cmd/notify` validates
all three itself through `config.RequireNotifications` and exits non-zero if any is
missing, so a misconfigured task fails loudly instead of running and sending nothing.

No job authenticates a caller, so **none of them takes `PORTAL_JWT_SECRET` or
`SERVICE_API_KEY`**; `config.RequireAuth` is called only by `cmd/cadenceos`. Do not add
them to either scheduled task. A job that holds a credential it never presents is a
standing credential in one more place for no benefit.

That rule is about presenting credentials, not about secrecy, which is what separates
the two cases: notify presents its Postmark key to Postmark, so it should hold it —
while `sweep` and `materialize` present it to nobody, which is exactly why notify does
not belong in `scripts/nightly.sh`.

## Coolify configuration

Create **two** scheduled tasks on the CadenceOS application, per environment.

| Field | Value |
| --- | --- |
| Name | `nightly` |
| Command | `./scripts/nightly.sh` |
| Frequency | `0 10 * * *` |
| Container | the CadenceOS app container |

| Field | Value |
| --- | --- |
| Name | `notify` |
| Command | `notify` |
| Frequency | `*/5 * * * *` |
| Container | the CadenceOS app container |

Both tasks inherit the application's environment, so `DATABASE_URL` and the Postmark
variables need no separate configuration and the production credentials never leave
Coolify's network.

`notify` is the bare binary name rather than a script path: the `Dockerfile` builds
`./cmd/...` — every command, not only the service — and copies the output to
`/usr/local/bin/`, so it is already on `PATH` in the deployed image. It needs no wrapper
because it is one job; `nightly.sh` exists to sequence two and to continue past a
failure in the first.

Running every five minutes is safe without any coordination between runs.
`ClaimNotifiableEvents` claims in a single statement whose CTE uses
`FOR UPDATE ... SKIP LOCKED`, and whose candidate predicate excludes rows whose
`last_attempt_at` falls inside the 15-minute visibility timeout. A run still working
when the next tick fires has its in-flight rows skipped, not re-sent. `cmd/notify` also
carries a 10-minute internal timeout, so a slow run can overlap the following two ticks
without incident.

**On the nightly schedule:** `0 10 * * *` is 10:00 UTC. The cron runs in UTC, so the
hour is chosen to be outside US business hours year-round *and* to keep a wide margin
from local midnight in the US zones, which is the part that actually matters.

An hour sitting on a local date boundary makes the run's own date ambiguous: a little
clock skew, a slow container start, or a DST shift moves it across midnight and the
job computes a different day than the one intended. 10:00 UTC is 03:00 PDT / 02:00 PST
on the west coast and 06:00 EDT / 05:00 EST on the east, so it stays hours clear of
midnight in both DST states.

For contrast, 07:00 UTC — the obvious "middle of the night" pick — is exactly 00:00 PDT
in summer and 23:00 PST *the previous day* in winter. Do not use it.

The jobs' own date arithmetic is separate from this: each schedule's dates are computed
in the customer's own timezone (`Service.today`), not the scheduler's. The hour above
is about the run being unambiguous, not about the cadence math.

The five-minute dispatch cadence needs no equivalent reasoning: it is a polling
interval, not a date boundary, and it computes nothing from the hour it runs at.

Anything other than the frequency belongs in this repository rather than in the Coolify
UI. If the nightly passes need to change, change `scripts/nightly.sh`; if dispatch
behaviour needs to change, change `cmd/notify` or `internal/notify`.

## Verifying

After configuring the task, confirm it actually runs — a scheduled task that was never
saved looks identical to one that runs cleanly, since neither produces an error.

Run it by hand first. From a checkout with `DATABASE_URL` set:

```
make nightly
```

Expected output on a healthy database:

```
nightly: <timestamp> sweep starting
{"level":"INFO","msg":"resume sweep complete","considered":0,"resumed":0,...}
nightly: <timestamp> sweep ok
nightly: <timestamp> materialize starting
{"level":"INFO","msg":"materialization complete","schedules_considered":2,"occurrences_created":0,...}
nightly: <timestamp> materialize ok
```

`occurrences_created: 0` on a second consecutive run is correct, not a failure — it is
the idempotency guarantee showing.

To confirm the deployed task has been running, check that active schedules still hold a
full horizon:

```sql
SELECT s.id, count(o.*) FILTER (WHERE o.status = 'planned') AS planned
FROM schedules s
LEFT JOIN occurrences o ON o.schedule_id = s.id
WHERE s.status = 'active'
GROUP BY s.id
HAVING count(o.*) FILTER (WHERE o.status = 'planned') < 3
ORDER BY planned;
```

Any row returned is a schedule below the horizon (`MATERIALIZE_HORIZON`, default 3),
which means the nightly pass is not reaching it. An empty result is the healthy state.

And for pauses that should have ended:

```sql
SELECT id, paused_until FROM schedules
WHERE status = 'paused' AND paused_until < current_date;
```

Also empty when the sweep is running.

### Notification dispatch

Run it by hand first, the same way. With `DATABASE_URL` and the three Postmark
variables set:

```
make notify
```

Expected output:

```
{"level":"INFO","msg":"notification dispatch complete","claimed":2,"sent":2,"skipped":0,"send_failed":0}
```

Run it a second time immediately. `claimed: 0` is correct, not a failure — it is the
claim predicate refusing to re-send what the first run already delivered, and it is the
property the five-minute cadence depends on.

To confirm the deployed task has been running, look for events whose email never went
out:

```sql
SELECT count(*) FROM schedule_events e
LEFT JOIN notification_log n ON n.schedule_event_id = e.id
WHERE e.event_type IN ('schedule.created', 'schedule.paused',
                       'schedule.resumed', 'schedule.canceled')
  AND (n.id IS NULL OR n.status = 'pending')
  AND e.created_at < now() - interval '1 hour';
```

Zero is the healthy state. Any row is an event the customer was never told about. The
one-hour grace keeps a five-minute cadence, a retry, and the 15-minute visibility
timeout from producing false alarms; a row older than that is not in flight.

Rows with `n.status = 'failed'` are a different problem — those were attempted and gave
up after `maxAttempts`, usually a bad address rather than a missing scheduler:

```sql
SELECT n.schedule_event_id, n.attempts, n.last_error, n.last_attempt_at
FROM notification_log n WHERE n.status = 'failed' ORDER BY n.last_attempt_at DESC;
```

## Alerting

**Not yet built.** Coolify records each task's exit status in its own history, but
nothing pages on a failure, so a job that has been failing for a week is currently as
quiet as one that was never scheduled. ADR 0011 accepts this as a known cost of not
using a CI-hosted cron, and ADR 0012 notes that a second task doubles the exposure. The
queries above are the manual stand-in until alerting exists.

## Running one job alone

Each job is still its own binary and can be run independently — useful when
investigating one of them:

```
make sweep         # end timed pauses only
make materialize   # top up the horizon only
make notify        # send outstanding transactional email only
```

`cmd/materialize` also takes `-today YYYY-MM-DD` to run as if it were another date,
which is how horizon behaviour is checked around month ends without waiting.
