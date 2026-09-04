# Scheduled jobs

Two passes move schedules forward every night with no customer action. Both run from
one script, `scripts/nightly.sh`, invoked by a Coolify scheduled task in staging and
production. The reasoning behind that choice — and the two options rejected — is
[ADR 0011](adr/0011-coolify-scheduled-tasks-for-nightly-jobs.md).

| Job | What it does | Spec |
| --- | --- | --- |
| `sweep` | Ends timed pauses that have come due, re-anchoring the schedule to today | §6 resume |
| `materialize` | Tops the planned-occurrence horizon back up for every active schedule | §5 step 1 |

Neither places an order. Arm, Execute and Reconcile (spec §5 steps 2–4) are Phase 2.

## Why this matters

The failure mode is silent. Nothing breaks on the first night the scheduler is missing.
The horizon drains one occurrence at a time as the portal consumes it, and customers
who paused until a date are never resumed. The symptom — an empty upcoming queue, a
schedule stuck paused past its date — shows up weeks after the cause.

So the check that matters is not "did tonight's run succeed" but "has a run happened at
all recently." See [Verifying](#verifying) below.

## What the script does

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

`DATABASE_URL` — and nothing else.

Neither job authenticates a caller, so neither takes `PORTAL_JWT_SECRET` or
`SERVICE_API_KEY`; `config.Load` no longer demands them and `config.RequireAuth` is
called only by `cmd/cadenceos`. **Do not add them to the scheduled task.** A job that
holds a credential it never presents is a standing credential in one more place for no
benefit.

## Coolify configuration

Create a scheduled task on the CadenceOS application, per environment:

| Field | Value |
| --- | --- |
| Name | `nightly` |
| Command | `./scripts/nightly.sh` |
| Frequency | `0 7 * * *` |
| Container | the CadenceOS app container |

The task inherits the application's environment, so `DATABASE_URL` needs no separate
configuration and the production credential never leaves Coolify's network.

**On the schedule:** `0 7 * * *` is 07:00 UTC — chosen to sit outside US business hours
year-round rather than on a round local number, since the cron runs in UTC and would
otherwise shift an hour against the merchant's day at each DST transition. The jobs
themselves are unaffected by that: each schedule's dates are computed in the customer's
own timezone (`Service.today`), not the scheduler's.

Anything other than the frequency belongs in this repository rather than in the Coolify
UI. If the two passes need to change, change `scripts/nightly.sh`.

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

## Alerting

**Not yet built.** Coolify records each task's exit status in its own history, but
nothing pages on a failure, so a nightly job that has been failing for a week is
currently as quiet as one that was never scheduled. ADR 0011 accepts this as a known
cost of not using a CI-hosted cron; the queries above are the manual stand-in until
alerting exists.

## Running one job alone

Each job is still its own binary and can be run independently — useful when
investigating one of them:

```
make sweep         # end timed pauses only
make materialize   # top up the horizon only
```

`cmd/materialize` also takes `-today YYYY-MM-DD` to run as if it were another date,
which is how horizon behaviour is checked around month ends without waiting.
