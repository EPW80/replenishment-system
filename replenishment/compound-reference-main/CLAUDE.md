# Project instructions

Guidance for Claude Code working in this repository.

> **CUSTOMIZE:** every section marked `CUSTOMIZE` below is a placeholder. Fill it
> in when you start the project. Leave the lifecycle rules as they are.

---

## Project

<!-- CUSTOMIZE -->
- **What this is:** _one sentence._
- **Stack:** _language, framework, package manager, runtime version._
- **Hosting:** _where staging and production run._
- **Owner:** _team or person._

## Commands

<!-- CUSTOMIZE — these must match the commands in .github/workflows/ -->
| Task | Command |
| --- | --- |
| Install | _fill in_ |
| Lint | _fill in_ |
| Test | _fill in_ |
| Build | _fill in_ |
| Run locally | _fill in_ |

Keep this table and the workflows in sync. If they drift, CI and local runs stop
agreeing and the AI review gate loses its reference point.

## Layout

<!-- CUSTOMIZE -->
_Describe the source layout once it exists: where application code lives, where
tests live, where configuration lives._

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
