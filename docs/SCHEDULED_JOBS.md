# Scheduled jobs

Three passes run with no customer action, each as its own Coolify scheduled task in
staging and production. Sweep and materialize run from one script, `scripts/nightly.sh`;
notify runs from a second, `scripts/notify.sh`. The reasoning behind running sweep and
materialize together — and the two options rejected for all three — is
[ADR 0011](adr/0011-coolify-scheduled-tasks-for-nightly-jobs.md). Notify is a separate
task rather than a third job in the same script; see [Why notify is separate](#why-notify-is-separate).

| Job | What it does | Spec |
| --- | --- | --- |
| `sweep` | Ends timed pauses that have come due, re-anchoring the schedule to today | §6 resume |
| `materialize` | Tops the planned-occurrence horizon back up for every active schedule | §5 step 1 |
| `notify` | Sends outstanding schedule created/paused/resumed/canceled emails | §7 |

Neither sweep nor materialize places an order. Arm, Execute and Reconcile (spec §5
steps 2–4) are Phase 2.

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

## Why notify is separate

`cmd/notify` sends the Phase 4 confirmation emails and is just as much an
unattended-and-idempotent nightly pass as sweep and materialize (delivery is
at-least-once, docs/adr/0010, so an overlapping or retried run is safe). It does not
run from `scripts/nightly.sh`, though, because it needs a credential neither of those
two jobs has any use for: `POSTMARK_API_KEY`, `NOTIFICATION_FROM_ADDRESS`, and
`NOTIFICATION_SUPPORT_CONTACT` (validated by `config.RequireNotifications`, called
only by `cmd/notify`). ADR 0011 scoped `scripts/nightly.sh` to `DATABASE_URL` alone on
exactly this reasoning — a scheduled task should not hold a credential it never
presents — so giving that script Postmark credentials for notify's sake would undo
the reason sweep and materialize are scoped the way they are. `scripts/notify.sh`
carries that credential instead, and only that one.

`cmd/notify` validates its own Postmark configuration and exits non-zero if it is
missing; `scripts/notify.sh` does not duplicate that check, only `DATABASE_URL`.

## Environment

For `scripts/nightly.sh` (sweep, materialize): `DATABASE_URL` — and nothing else.
Neither job authenticates a caller, so neither takes `PORTAL_JWT_SECRET` or
`SERVICE_API_KEY`; `config.Load` no longer demands them and `config.RequireAuth` is
called only by `cmd/cadenceos`. **Do not add them to the scheduled task.** A job that
holds a credential it never presents is a standing credential in one more place for no
benefit.

For `scripts/notify.sh`: `DATABASE_URL`, `POSTMARK_API_KEY`, `NOTIFICATION_FROM_ADDRESS`,
and `NOTIFICATION_SUPPORT_CONTACT`. Still never `PORTAL_JWT_SECRET` or
`SERVICE_API_KEY` — notify authenticates nothing either.

## Coolify configuration

Create two scheduled tasks on the CadenceOS application, per environment:

| Field | Value |
| --- | --- |
| Name | `nightly` |
| Command | `./scripts/nightly.sh` |
| Frequency | `0 10 * * *` |
| Container | the CadenceOS app container |

| Field | Value |
| --- | --- |
| Name | `notify` |
| Command | `./scripts/notify.sh` |
| Frequency | `0 10 * * *` |
| Container | the CadenceOS app container |

`notify` is scheduled alongside `nightly` rather than depending on it: sweep's resume
can itself create a notifiable event (a resumed schedule), and running notify in the
same window means that email does not wait for a second night. The two tasks are
independent processes, so their relative order within the window is not guaranteed;
that is fine, since notify only ever sends for events already committed to
`schedule_events`, whichever of the two runs first.

The task inherits the application's environment, so `DATABASE_URL` needs no separate
configuration and the production credential never leaves Coolify's network.

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
UI. If the sweep/materialize pair needs to change, change `scripts/nightly.sh`; if
notify's behavior needs to change, change `scripts/notify.sh` or `internal/notify`.

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

Then notify, with `DATABASE_URL`, `POSTMARK_API_KEY`, `NOTIFICATION_FROM_ADDRESS`, and
`NOTIFICATION_SUPPORT_CONTACT` all set:

```
make notify
```

Expected output on a healthy database:

```
{"level":"INFO","msg":"notification dispatch complete","claimed":0,"sent":0,"skipped":0,"send_failed":0}
```

`claimed: 0` is correct whenever there is nothing outstanding — it does not mean the
job failed to find work.

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

And for notify, rows stuck past their max attempts or sitting `pending` well beyond
the dispatcher's 15-minute visibility timeout (`internal/notify.visibilityTimeout`):

```sql
SELECT schedule_event_id, status, attempts, last_error, last_attempt_at
FROM notification_log
WHERE status = 'failed'
   OR (status = 'pending' AND last_attempt_at < now() - interval '30 minutes')
ORDER BY last_attempt_at;
```

Any row returned is a notification that has not resolved after the task should have
had a chance to. An empty result is the healthy state.

## Alerting

**Not yet built.** Coolify records each task's exit status in its own history, but
nothing pages on a failure, so either scheduled task failing for a week is currently as
quiet as one that was never scheduled. ADR 0011 accepts this as a known cost of not
using a CI-hosted cron; the queries above are the manual stand-in until alerting
exists.

## Running one job alone

Each job is still its own binary and can be run independently — useful when
investigating one of them:

```
make sweep         # end timed pauses only
make materialize   # top up the horizon only
make notify        # send outstanding confirmation emails only
```

`cmd/materialize` also takes `-today YYYY-MM-DD` to run as if it were another date,
which is how horizon behaviour is checked around month ends without waiting.
