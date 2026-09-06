# Workflows

Eleven files: nine reusable workflows implementing the lifecycle stages, and two
callers that wire them together.

```
pr-checks.yml  (on: pull_request, push)
  ├─ lint.yml
  ├─ test.yml
  ├─ build.yml
  └─ security-check.yml

deploy.yml     (on: workflow_dispatch)
  ├─ validate                       ← inline pre-flight, no environment
  ├─ staging-deploy.yml
  ├─ staging-health-check.yml
  ├─ production-deploy.yml          ← Environment gate = business approval
  └─ production-health-check.yml

```

There is no rollback workflow. Rollback is manual — see
[`LIFECYCLE.md` §12](LIFECYCLE.md#12-rollback) for why, and for what would make it
automatable.

| Workflow | Triggers | State | Job permissions |
| --- | --- | --- | --- |
| `lint.yml` | call | **Functional** — `make lint` | `contents: read` |
| `test.yml` | call | **Functional** — `make migrate` + `make test` against a `postgres:16` service | `contents: read` |
| `build.yml` | call | **Functional** — `make build` | `contents: read` |
| `security-check.yml` | call | **Functional** — `make security` | `contents: read` |
| `staging-deploy.yml` | call | **Functional** — posts the Coolify webhook; needs the `staging` Environment (#40) | `contents: read` |
| `staging-health-check.yml` | call | **Functional** | none |
| `production-deploy.yml` | call | **Functional** — posts the Coolify webhook; needs the `production` Environment (#40) | `contents: read` |
| `production-health-check.yml` | call | **Functional** | none |
| `pr-checks.yml` | PR, push | Functional caller | `contents: read` |
| `deploy.yml` | dispatch | Functional caller | `contents: read`; its `validate` job none |

---

## Why placeholders fail instead of passing

A placeholder that exits `0` produces a green check for a lint run that never
happened and a test suite that never ran. Branch protection would be satisfied,
the PR would look fully verified, and the release metadata would record
`test_status: passed`.

That failure mode is silent and survives indefinitely — nobody investigates a
green check. A red one is investigated immediately, and the error message names
the file to edit. So a fresh copy of this template has red CI until it is
configured, and that is the intended state.

The same reasoning applies to the deploy workflows, more sharply: a deploy step
that exits `0` without deploying would let `production-health-check` pass against
the *previously* deployed version, reporting a successful release of code that was
never shipped.

## What is functional as shipped

**The health checks.** An HTTP probe with retries does not depend on the
language, framework, or host, so there is nothing to guess. Give them a URL and
they work.

**The guards inside the placeholder deploys**, even though the deploy step itself
fails:

- `deploy.yml`'s `validate` job — rejects a malformed or mismatched SHA before
  anything is deployed. It exists because `production-deploy` binds the
  production Environment at job level, so that workflow's own check runs only
  *after* a reviewer has approved — a mismatch caught there would waste the
  approval rather than prevent it. This job holds no environment and runs first.
- `staging-deploy.yml` — rejects anything that is not a full 40-character SHA.
- `production-deploy.yml` — rejects a mismatch between `commit-sha` and
  `approved-sha`. Still the authoritative gate; `validate` only fails faster.
(`rollback.yml` carried the same kind of guard before it was deleted; rollback is
now manual, per [`LIFECYCLE.md` §12](LIFECYCLE.md#12-rollback).)

Keep these when you fill in the deploy commands. They are what makes an approval
refer to a specific version of the code.

---

## Least-privilege permissions

Every **reusable** workflow sets `permissions: {}` at the top level, denying the
whole token, then grants the narrowest scope on the jobs that need it.

The two callers are the exception: they set `contents: read` at the top level,
because a called workflow can only narrow the caller's token, never widen it. A
caller with `permissions: {}` would make every job it calls fail. Their top-level
grant is therefore the ceiling for everything downstream, which is why it is kept
to the single scope `actions/checkout` needs.

Three rules worth knowing:

1. **`contents: read` is for `actions/checkout`, nothing else.** The two health
   checks do not check out the repository, so they grant nothing at all —
   `permissions: {}` on the job.

2. **A reusable workflow can narrow the caller's token, never widen it.** If you
   add `security-events: write` to `security-check.yml`, you must also add it to
   the `security-check` job in `pr-checks.yml`, or the job fails. The commented
   lines are placed in both files.

3. **Set the repository default to read-only.** Under *Settings → Actions →
   General → Workflow permissions*. Without it, a workflow that omits a
   `permissions:` block gets a write-scoped token by default.

### Secrets

Deploy credentials are stored as **Environment** secrets and read inside the
deploy workflow, by the job that binds the environment:

```yaml
# in staging-deploy.yml / production-deploy.yml
environment:
  name: ${{ inputs.environment }}
...
    - run: ./deploy.sh
      env:
        TOKEN: ${{ secrets.YOUR_SECRET_NAME }}
```

They are deliberately **not** forwarded from `deploy.yml`. There is a trap here
worth understanding, because it fails silently:

- `environment:` is not a valid key on a job that calls a reusable workflow.
- A job with no environment cannot read environment-scoped secrets.
- An unset secret resolves to an **empty string**, not an error.

So `secrets: { TOKEN: ${{ secrets.PROD_TOKEN }} }` in a caller job passes an empty
value, the deploy runs with no credential, and the only symptom is a confusing
failure inside the deploy tool. Use the same secret name on both Environments with
different values — the environment binding provides the isolation, not the name.

Never `secrets: inherit` — it hands the called workflow every secret in the
repository, including production credentials to a staging job.

Prefer OIDC to a stored token where the provider supports it. The commented
`id-token: write` lines mark where; a short-lived token exchanged at run time
leaves no standing credential to leak.

### Checkout hardening

Checkout steps use `persist-credentials: false`, so the Actions token is not
written into `.git/config` where later steps — including anything a dependency
runs — could read it.

---

## Third-party actions

Only `actions/checkout`, published by GitHub, is used, and it is pinned to the
full commit SHA `3d3c42e5aac5ba805825da76410c181273ba90b1` (`v7.0.1`) rather than
the mutable `@v7` tag. Every action runs inside the job with access to that job's
token and secrets, which makes each one a supply-chain dependency — and a tag can
be repointed at different code without any change to this repository.

Actions that would normally be useful are documented in
[`RECOMMENDED_ACTIONS.md`](RECOMMENDED_ACTIONS.md) rather than installed. If you
adopt one, pin it to a full commit SHA — a tag can be moved to point at different
code.

---

## Concurrency

`pr-checks.yml` cancels superseded runs **only for pull requests**, where a new
push makes the previous run's result irrelevant. Runs on the default branch are
never cancelled: each commit there keeps its own pass/fail record, which is what
you consult when deciding whether a SHA is releasable.

`deploy.yml` uses `cancel-in-progress: false` on a single `deploy` group. Deploys
queue and never cancel each other — interrupting a deploy mid-flight can leave a
partially applied release, which is worse than waiting.

## Conventions when extending

- One concern per workflow; compose with `workflow_call`.
- **All inputs use `kebab-case`**, under every trigger. One rule, because a
  workflow can expose both `workflow_call` and `workflow_dispatch` and the job
  body reads the same `inputs.<name>` either way — two conventions would force it
  to handle two spellings of the same value. Note that the release metadata
  fields are `snake_case`; the input names deliberately do not track them.
- Pass untrusted or user-supplied values through `env:` rather than interpolating
  `${{ }}` directly into a `run:` script — interpolation happens before the shell
  sees the line, so a crafted value could otherwise inject a command.
- Every job sets `timeout-minutes`.
- Deploy targets are always full SHAs, never branches or tags.
