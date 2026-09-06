# Delivery lifecycle

The path every change takes, and what enforces each step.

```
issue
  └─> feature branch
        └─> pull request
              └─> automated checks      lint · test · build · security-check
                    └─> AI/security review
                          └─> peer approval
                                └─> staging deploy + health check
                                      └─> business approval (where required)
                                            └─> production deploy
                                                  └─> production health check
```

There is no automated rollback step. See §12.

A step "enforced by GitHub" cannot be skipped by editing a file in this
repository. A step enforced by convention can — those depend on review.

| Step | Enforced by | Configured where |
| --- | --- | --- |
| Issue first | Convention | `CONTRIBUTING.md`, issue forms |
| Feature branch | GitHub | Branch protection: no direct push |
| Pull request | GitHub | Branch protection: require PR |
| Automated checks | GitHub | Branch protection: required status checks |
| AI/security review | Convention | `.claude/agents/security-reviewer.md` |
| Peer approval | Convention *(GitHub once configured)* | `CODEOWNERS.example` — not yet operational |
| Staging | Convention | `deploy.yml` ordering |
| Business approval | GitHub | Required reviewers on `production` Environment |
| Production | GitHub | Environment gate + SHA match check |
| Health check | GitHub | `deploy.yml` job dependency |
| Rollback | Human decision | Manual — see §12; there is no rollback workflow |

---

## 1. Issue

Work starts with an issue, using a form from `.github/ISSUE_TEMPLATE/`. The issue
number goes in the branch name, so the change is traceable from any commit back
to the reason it was made.

## 2. Feature branch

`<type>/<issue-number>-<slug>`, branched from the default branch. Direct commits
to the default branch are blocked by branch protection.

## 3. Pull request

The author fills in `.github/PULL_REQUEST_TEMPLATE.md`. Its risk section is not
ceremony — the answers become `database_migration`, `rollback_safe`, and
`business_approval_required` in the release metadata, and those values determine
whether an automated rollback is available later.

## 4. Automated checks

`pr-checks.yml` runs four reusable workflows in parallel:

| Check | Gate |
| --- | --- |
| `lint` | Style and static correctness |
| `test` | Behaviour |
| `build` | The project still builds |
| `security-check` | Dependency audit, static analysis |

All four must pass. They are **not yet configured as required status checks** —
see [Required status checks](#required-status-checks--deferred) — so until that is
done they are advisory and a PR can technically merge red.

These ship as placeholders that **fail** until customized. A placeholder that
exited `0` would show a green check for work that never happened; see
[`WORKFLOWS.md`](WORKFLOWS.md).

## 5. AI/security review

An AI pass reviews the diff using
[`.claude/agents/security-reviewer.md`](../.claude/agents/security-reviewer.md):
secrets, untrusted input, authorization, CI/CD and supply-chain changes.

It is a signal, not an approver. It has no merge authority and does not replace
the human reviewer — an AI review cannot be accountable for a decision, which is
what an approval is. Its value is catching mechanical mistakes before a human
spends attention on the PR.

## 6. Peer approval

At least one human owner approves. Authors cannot approve their own work, which is
also why the release metadata requires `human_reviewer` to differ from `developer`.

**Not yet enforced by GitHub.** `CODEOWNERS.example` is still a template; it becomes
`.github/CODEOWNERS` once real teams exist and have repository access, per
`docs/CUSTOMIZATION.md` step 2. A `CODEOWNERS` naming a team without write access is
silently ignored, so configuring it early would produce a gate that looks active and
enforces nothing. Until then this step depends on people, not tooling.

When you do enable it, also enable **Dismiss stale approvals when new commits are
pushed** — otherwise an approval granted on one commit silently carries over to code
pushed afterwards.

Until then, a merge with no second human reviewer simply does not get a
release-metadata record — see [`RELEASE_METADATA.md`](RELEASE_METADATA.md#no-second-reviewer-no-record).

## 7. Merge and release record

On merge, create the release metadata record for the merge commit SHA. From here
on, that SHA is the release's identity. See
[`RELEASE_METADATA.md`](RELEASE_METADATA.md).

## 8. Staging

A maintainer dispatches `deploy.yml` with the commit SHA and the approved SHA
from the release record. A `validate` job checks both are full SHAs and that they
match before anything deploys, so a bad input fails in seconds rather than after
a staging deploy or, worse, after someone has approved it.

It then runs `staging-deploy`, followed by `staging-health-check` against the URL
the deploy reported.

The health check is a real HTTP probe and works as shipped. It proves the process
is serving; it does not prove the release is correct. Verify the actual change on
staging before promoting.

## 9. Business approval

For releases where `business_approval_required` is `true`.

This gate is the **required reviewer rule on the `production` GitHub
Environment**, not a field in a file. GitHub pauses the run before any step of
`production-deploy` executes and waits for a named reviewer to approve in the UI.

The approval refers to the exact `commit_sha` in the release record. That SHA
cannot drift — `production-deploy.yml` compares `commit-sha` against
`approved-sha` and fails if they differ, so an approval can never be applied to
code that was not reviewed.

The run only reaches this gate after a verified staging deploy, so no one is asked
to approve something untested.

## 10. Production

`production-deploy` checks out the exact approved SHA and deploys. Deploy the
artifact verified on staging where the platform allows it; rebuilding means
production runs a build nobody tested.

If the release includes a migration, it is applied here, and the record's
`database_migration` and `rollback_safe` values must already reflect that.

## 11. Production health check

`production-health-check` probes the deployment. Its failure is the trigger for a
rollback decision — it is intentionally the last automated step, and it does not
roll back on its own.

## 12. Rollback

**There is no rollback workflow. Rollback is a manual operation.**

`rollback.yml` existed as a stub and was deleted, because the deploy mechanism
cannot do what it promised. Its input was the SHA of the last known-good release,
but the Coolify deploy webhook takes no commit SHA — it builds whatever the branch
it tracks currently points at
([`DEPLOYMENT.md`](DEPLOYMENT.md#what-the-deploy-step-actually-does)). Wiring the
same webhook into it would have rebuilt *the broken release being rolled back
from*, and reported success while doing it. During an incident, that is worse than
having nothing: the guidance this section used to carry — a workflow that pretends
to roll back is worse than none, because people plan around it — applied to itself.

**What to do instead, today.** Recovering means getting the tracked branch back to
the known-good commit and redeploying it, which is a human-planned operation
because it means changing shared history. Check the release record's
`rollback_safe` first; if it is `false` — usually a destructive migration — a
redeploy is not recovery at all and the database state has to be planned for
separately.

**What would make it automatable.** Deploying a per-commit registry image tag
rather than rebuilding from a branch. Rollback then becomes "deploy tag X," a
genuine rollback rather than a rebuild of whatever the branch happens to point at.
[`DEPLOYMENT.md`](DEPLOYMENT.md) already names moving to a registry image as the
better shape; this is a second reason to want it. Reintroduce a rollback workflow
when that exists, not before.

**What has not changed:** rollback is still never automatic on a failed health
check. That was always deliberate and remains right — the health check cannot
distinguish "this release is broken" from "a dependency is briefly down," and
rolling back during a migration can compound the damage. Deciding to roll back
needs a human who can see the blast radius.

---

## Configuration checklist

The lifecycle depends on repository settings as much as on these files. None of
the GitHub-enforced steps work until these are set:

**Settings → Actions → General**
- [ ] Workflow permissions: **Read repository contents permission**
- [ ] Do not allow Actions to create or approve pull requests

**Settings → Branches → default branch**
- [ ] Require a pull request before merging
- [ ] Require approval from Code Owners — **deferred**, pending real teams; see
      `docs/CUSTOMIZATION.md` step 2
- [ ] Dismiss stale approvals on new commits
- [ ] Require status checks — **deferred on purpose; do not configure yet.**
      See "Required status checks" below.
- [ ] Require branches to be up to date before merging
- [ ] Block force pushes and deletions

**Settings → Environments**
- [ ] `staging` — staging deploy secrets, no reviewers
- [ ] `production` — production deploy secrets, **required reviewers set**
- [ ] Restrict `production` deploys to the default branch

### Required status checks — deferred

Not configured yet, and deliberately not guessed.

A job that calls a reusable workflow produces a check named
`<caller job> / <called job>`, so the names here are *expected* to be
`lint / Lint`, `test / Test`, `build / Build`, `security-check / Security check`.
That is a prediction about GitHub's naming, not an observation.

The failure mode if the prediction is wrong is bad in a specific way: a required
check whose name matches nothing is **silently never enforced**. Branch protection
reports itself as configured, PRs merge without the gate, and nothing appears
broken. That is precisely the false-green outcome this template is built to avoid,
so it is not a guess worth making.

**Sequence:**

1. Push to a real GitHub repository.
2. Open a throwaway pull request and let `pr-checks.yml` run.
3. Read the exact check names off that run's check list.
4. *Then* add those observed strings as required status checks.
5. Confirm with a second PR that a failing check actually blocks merge.

Until step 4, the four automated checks run and are visible, but do not block a
merge. Treat the gate as advisory until you have verified it bites.

**Settings → Code security**
- [ ] Secret scanning + push protection
- [ ] Dependency alerts
- [ ] Private vulnerability reporting (referenced by `SECURITY.md`)
- [ ] CodeQL, if it supports the chosen language
