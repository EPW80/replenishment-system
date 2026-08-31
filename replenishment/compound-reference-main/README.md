# Project Helix — Repository Template

Baseline repository structure for new Project Helix software projects.

This repository is **not an application**. It contains no runtime code, no
dependency manifest, and no language toolchain. It is a starting point you copy
and then customize.

---

## What you get

| Path | Purpose |
| --- | --- |
| `.github/workflows/` | Nine reusable workflows covering the delivery lifecycle, plus two thin callers |
| `.github/ISSUE_TEMPLATE/` | Issue forms that feed the lifecycle |
| `.github/PULL_REQUEST_TEMPLATE.md` | PR checklist tied to the review gates |
| `.claude/agents/` | Agent definitions for the AI review gate |
| `.claude/skills/` | Skills for release-metadata handling |
| `docs/` | Lifecycle, customization, workflow and release-metadata documentation |
| `docs/schemas/` | Release metadata JSON Schema + example record |
| `CODEOWNERS.example` | Template for the peer-approval gate (not yet operational) |

---

## The workflows start red, on purpose

Every check and deploy workflow in `.github/workflows/` is a **placeholder that
exits non-zero** with a message naming the file to edit.

This is deliberate. A placeholder that exits `0` would report a green check for
a lint run that never happened, a test suite that never ran, and a deploy that
never deployed. False green is worse than red. CI stays red until you replace
the marked `CUSTOMIZE` step in each workflow with a real command.

Two workflows are exceptions and work as shipped, because an HTTP probe does not
depend on the language or host:

- `staging-health-check.yml`
- `production-health-check.yml`

See [`docs/WORKFLOWS.md`](docs/WORKFLOWS.md) for the inputs and permissions of
each workflow, and [`docs/CUSTOMIZATION.md`](docs/CUSTOMIZATION.md) for what to
change per stack.

---

## Getting started

1. Copy this repository (or use it as a GitHub template repository).
2. Read [`docs/CUSTOMIZATION.md`](docs/CUSTOMIZATION.md) and work the checklist
   for your stack.
3. Leave `CODEOWNERS.example` as it is for now — see
   [`docs/CUSTOMIZATION.md`](docs/CUSTOMIZATION.md) step 2. It becomes
   `.github/CODEOWNERS` only once real teams exist and have repository access.
4. Create the `staging` and `production` GitHub Environments, and add required
   reviewers to `production` — that is where the business-approval gate lives.
5. Replace each `CUSTOMIZE` block in `.github/workflows/`.
6. Fill in the `CUSTOMIZE` sections of `CLAUDE.md` so AI review knows the stack.
7. Delete the parts of this README that no longer apply and describe the actual
   project.

---

## Design constraints

These rules are why the template looks the way it does. Keep them when you
extend it.

- **No third-party GitHub Actions.** Only `actions/checkout`, published by
  GitHub, is used, pinned to a full commit SHA rather than a movable tag. Useful third-party actions are documented as recommendations
  in [`docs/RECOMMENDED_ACTIONS.md`](docs/RECOMMENDED_ACTIONS.md), not installed.
- **Least-privilege permissions.** Every reusable workflow sets `permissions: {}`
  at the top level and grants the narrowest scope per job. The two callers grant
  `contents: read`, the ceiling their called workflows need.
- **No invented stack details.** Nothing here assumes a language, framework,
  package manager, host, or deploy provider.
- **Immutable commit SHA.** An executive approval always refers to the exact
  commit that was reviewed. See
  [`docs/RELEASE_METADATA.md`](docs/RELEASE_METADATA.md).

---

## Deliberate omissions

The `.gitignore` is intentionally minimal — it excludes only
`.claude/settings.local.json`, which is machine-specific and must not propagate to
projects copied from this template.

There is no dependency manifest, no linter config, and no editor config. All three
are stack-specific, and a wrong one is worse than a missing one. Add them, plus your
stack's `.gitignore` entries, as your first commit after copying.
