# 0011 — Coolify scheduled tasks for the nightly jobs

**Status:** Accepted

## Context

`cmd/materialize` (tops up the planned-occurrence horizon, spec §5 step 1) and
`cmd/sweep` (ends timed pauses that have come due, spec §6 resume) are both real,
idempotent one-off jobs. Both are documented in their own file headers and in
`CLAUDE.md` as intended to run nightly. Nothing ran them.

The failure this creates is silent and gradual rather than loud. Nothing breaks the day
the scheduler is missing; instead every active schedule's horizon drains one occurrence
at a time as the portal consumes it, and a customer who pauses until a date is never
resumed. By the time the symptom is visible — an empty upcoming queue, a schedule stuck
paused past its date — it has been wrong for weeks.

Issue #23 raised this and deliberately did not pick a mechanism, because the choice
turns on hosting facts rather than on anything in the code: where the production
`DATABASE_URL` may be reached from.

## Decision

**The nightly jobs run as a Coolify scheduled task, alongside the deployed service,
invoking `scripts/nightly.sh`.**

The deciding factor is credential blast radius. These jobs need database access and
nothing else, and Coolify already holds `DATABASE_URL` for the service it deploys into
the same network. A scheduled task there reuses a credential that exists, in a place
that already has it.

**Rejected: a scheduled GitHub Actions workflow.** This was the more attractive option
on every axis except the one that matters. It would be versioned in this repository,
reviewed as code, and its failures would surface in the Actions tab with no extra
alerting to build. It was rejected because a GitHub-hosted runner reaching the
production database means the production database accepts connections from GitHub's
published IP ranges, and it means a long-lived production `DATABASE_URL` living as an
Environment secret — a standing credential in a second system, held for the benefit of
a job that runs for two seconds a day. That is a materially larger exposure than the
schedule is worth. `CLAUDE.md` rule 7 permits the secret; the objection is not that it
would be hardcoded but that it would exist at all.

**Rejected: a goroutine inside `cmd/cadenceos`.** No external scheduler and no extra
credential anywhere, which is genuinely appealing. Rejected because it couples batch
work to the web process: the passes stop whenever the service is down or mid-deploy,
and running a second replica means two processes doing the same nightly work, which
needs leader election to be correct. The row locks in ADR 0007 would keep that safe
rather than corrupting state, but "safe" is not "correct" — it would be two jobs racing
to do one job's work. Spec §12 also wants a second brand to be a configuration change;
a scheduler welded into the service binary is the opposite of that.

**The order is sweep, then materialize, and both run even if the first fails.**
`scripts/nightly.sh` encodes this. The two jobs are independent — `Service.Resume`
re-materializes inside its own transaction, so a failed sweep cannot leave a resumed
schedule without occurrences — but the order still matters for legibility: running
sweep first means materialize's `schedules_considered` count is a true snapshot of the
active set including anything resumed that night. Running both regardless of the
first's outcome is the substantive half: materialize is the job whose absence drains
the horizon, and skipping it because sweep failed would turn one recoverable error into
the exact silent decay this ADR exists to prevent. The script exits non-zero if either
job failed, so the scheduler still reports the run as failed.

**The jobs take `DATABASE_URL` and nothing else.** Neither authenticates a caller, so
neither receives `PORTAL_JWT_SECRET` or `SERVICE_API_KEY` (issue #32 moved that
validation to `config.RequireAuth`, called only by `cmd/cadenceos`). A scheduled task
should not hold credentials it never presents.

## Consequences

- **The schedule itself is not versioned in this repository.** This is the real cost of
  the decision and it is not fully mitigable: the cron expression and the command live
  in Coolify's UI, so they can drift from `docs/SCHEDULED_JOBS.md` with nothing failing
  a build. What is versioned is everything the task invokes — the script, the order,
  the failure policy — so drift is confined to timing and to whether the task exists at
  all. `docs/SCHEDULED_JOBS.md` carries the exact configuration to check against, and
  verifying it belongs in the staging and production deploy checklists.
- **Failure alerting has to be built separately.** A GitHub Actions cron would have
  given a red run and an email for free. Coolify's task history shows the exit status,
  but nothing pages on it, so a failing nightly job is as quiet as no job at all until
  alerting exists. This is tracked as its own work rather than assumed.
- `scripts/nightly.sh` is POSIX `sh` and resolves each job as a compiled binary before
  falling back to `go run`, because no `Dockerfile` is checked in and the runtime image
  is not known here. If the image is later pinned down, the fallback can go.
- Running the pair by hand is `make nightly`, which is the same script — so a local
  reproduction and the production run cannot diverge.
- The choice is per-deployment configuration, not architecture. A second brand on
  different hosting can schedule the same script however that platform prefers without
  touching the service.
