# Project instructions

Guidance for Claude Code working in this repository.

---

## Project

- **What this is:** CadenceOS — a standalone scheduling service that turns one-time
  WooCommerce purchases into recurring orders on a customer-controlled cadence, with
  pause, skip, defer, and cadence-change built in.
- **Stack:** Go (toolchain pinned in `go.mod`), PostgreSQL 16, `river` for the durable
  queue, `goose` for migrations, `sqlc` + `pgx` for data access. Postmark for
  transactional email. No framework.
- **Hosting:** Hetzner via Coolify, alongside PartnerOS. Staging and production are
  separate GitHub Environments; deploy credentials are Environment-scoped secrets.
- **Owner:** Erik Williams.

The service owns schedules. WooCommerce owns catalog, checkout, payment, and
fulfillment. That split is deliberate — it is what lets the same service deploy behind
a second brand as a configuration change. See `docs/replenishment-service-spec.md` §1
and §12.

## Commands

| Task | Command |
| --- | --- |
| Install | `make deps` |
| Lint | `make lint` |
| Test | `make test` |
| Build | `make build` |
| Run locally | `make run` |

Keep this table and the workflows in sync. If they drift, CI and local runs stop
agreeing and the AI review gate loses its reference point. Every command above is a
`Makefile` target, and the workflows call the same targets — so there is one
definition of what "lint" means, not two.

Supporting targets: `make security` (dependency audit), `make migrate` (apply
migrations), `make materialize` (run the occurrence horizon job), `make notify` (run
one lifecycle-email dispatch pass), `make db-up` / `make db-down` (local Postgres).
`cmd/export` (read-model CSV export) takes required flags and has no fixed-argument
`make` target — run it directly, see its `-h`.

## Layout

```
cmd/cadenceos/         Service entrypoint: HTTP server, graceful shutdown, /healthz
cmd/migrate/           Applies pending migrations (deploy step and CI)
cmd/materialize/       Nightly occurrence-horizon job (spec §5 step 1)
cmd/export/            Read-model CSV export (spec §8), no HTTP/auth — see
                       internal/readmodel and docs/adr/0006
cmd/notify/            Lifecycle-email dispatch pass (spec §7, partial) — see
                       internal/notify and docs/adr/0007
internal/domain/       Entities, pure cadence math, and the spec §6 state-transition
                       preconditions. No I/O — keeps the date arithmetic and
                       transition rules testable without a database.
internal/store/        Data access (sqlc + pgx) behind a Repository interface
internal/store/migrations/   goose SQL migrations, including the read-model views
internal/materialize/  Occurrence horizon job, behind a Queue interface
internal/schedule/      Spec §6 state-machine service: pause, resume, skip_next,
                        defer, change_cadence, cancel. Property-based tests over
                        transition sequences.
internal/readmodel/    Spec §8 read models — cadence distribution, churn reasons,
                       occurrence forecast, audience segments, cohort retention —
                       queried from views, never the write tables directly
internal/notify/       Spec §7 lifecycle email (partial) — Postmark client, templates,
                       and a transactional-outbox dispatcher off schedule_events
internal/httpapi/      HTTP handlers and middleware (schedules, transitions, health)
internal/config/       Environment-driven configuration
internal/compliance/   Forbidden-identifier guard (see below) and its test
internal/testsupport/  Test-only database fixtures (private schema per test)
docs/                  Lifecycle, workflow, and release-metadata documentation
docs/adr/              Architecture decision records
docs/replenishment-service-spec.md   The technical spec this service implements
releases/              Append-only release metadata records
```

Third-party choices (`river`, `goose`, `sqlc`) each sit behind a narrow interface, so
replacing one is an adapter swap rather than a rewrite. The spec directs reuse of
PartnerOS's queue and REST scaffolding; where that was not reachable, the ADRs in
`docs/adr/` record what was chosen instead and why.

---

## Compliance boundary

**Read this before designing any schema, endpoint, email, or UI string.** It is a hard
constraint from `docs/replenishment-service-spec.md` §2, not a preference.

The service stores `interval_days` and nothing that implies consumption. It must never
store, compute, infer, or display:

- units-per-day, servings-per-day, or any usage rate
- doses remaining, "you have 4 days left," or supply-depletion projections
- adherence streaks, missed-dose reminders, or intake logging
- outcome tracking, symptom logs, or goal progress
- any per-compound cadence *recommendation*, whether authored or model-generated

A default cadence per SKU is fine — it is a merchandising decision derived from pack
size and observed reorder behavior, and it must be documented as such in the SKU
config, in those words.

**Copy rule for every surface: "when to reorder," never "when to take."** This applies
to response fields, error strings, email templates, and portal copy alike.

The instant a field named `doses_per_day` appears in a migration, this stops being a
commerce tool and becomes a treatment app: FTC/FDA exposure, processor risk, and a
materially different insurance conversation.

This is enforced mechanically by `internal/compliance`, which fails the build on a
forbidden identifier and runs as part of `make test`. Do not weaken that guard to make
a change pass — extend the change instead. Reject any PR that adds such a column.

---

## Lifecycle rules

These apply regardless of stack. Do not remove them.

1. **Work starts from an issue.** No branch without a tracking issue.
2. **Branch from the default branch.** Naming: `<type>/<issue-number>-<slug>`,
   e.g. `feat/142-token-refresh`. Types: `feat`, `fix`, `chore`, `docs`, `refactor`.
3. **Never commit directly to the default branch.** Every change lands via pull
   request.
4. **One PR, one concern.** If a PR needs two paragraphs to explain why it
   touches unrelated areas, split it.
5. **Do not edit `.github/workflows/` as a side effect** of a feature change.
   Workflow changes ship in their own PR so they are reviewed on their own terms.
6. **Do not weaken a `permissions:` block** to make a job pass. Escalating a
   workflow's token scope is a security change and needs its own PR and review.
7. **Never hardcode a secret.** Read from GitHub Actions secrets or the
   platform's secret store. If you need a new secret, say so in the PR body
   rather than inventing a value.
8. **Release metadata is append-only.** Records in `releases/` are written once
   and never edited. `commit_sha` identifies the record. See
   [`docs/RELEASE_METADATA.md`](docs/RELEASE_METADATA.md).

## Database migrations

Any change that adds, alters, or drops schema must:

- set `database_migration: true` in the release metadata record,
- state in the PR body whether the migration is backward compatible,
- state whether rollback is safe; if it is not, set `rollback_safe: false`.

A destructive migration makes a release non-rollback-safe. Say so explicitly
rather than leaving it implied.

Two invariants this schema depends on, both from spec §3:

- **`occurrences.idempotency_key` is `UNIQUE`.** It is `schedule_id:sequence_no`, and
  it is the whole safety story for order creation — a retry, a duplicate queue
  delivery, or a redeploy mid-run must never produce a second charge. Never drop or
  weaken that constraint.
- **`next_run_date` is always recomputed as `anchor_date + (n × interval_days)`**,
  never as `last_run + interval`. Incremental addition accumulates drift across skips,
  deferrals, and retries; anchor-relative computation does not.

`payment_token_ref` holds an opaque gateway vault reference. Card data never enters
this system.

## Review gates

A change reaches production only after all of these:

| Gate | Who or what |
| --- | --- |
| Automated checks | `lint`, `test`, `build`, `security-check` |
| AI review | `.claude/agents/security-reviewer.md` |
| Peer approval | a human owner (see `CODEOWNERS.example`; not yet operational) |
| Staging verification | `staging-deploy` + `staging-health-check` |
| Business approval | required reviewer on the `production` Environment |
| Production health | `production-health-check` |

Full description: [`docs/LIFECYCLE.md`](docs/LIFECYCLE.md).

## When asked to deploy

Do not run deploy workflows on your own initiative. Deploys are triggered by a
human via `workflow_dispatch`, and production additionally requires the
Environment reviewer to approve. If asked to "ship it," prepare the release
metadata record and report what is still missing.
