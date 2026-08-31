# 0005 — Enforce the compliance boundary with a build-failing guard

**Status:** Accepted

## Context

Spec §2 draws a hard line: the service stores `interval_days` and nothing implying
consumption. No usage rates, doses remaining, supply projections, adherence tracking,
intake logging, outcome tracking, or per-compound cadence recommendations.

This is not a style preference. Spec §2: "The instant a field named `doses_per_day`
appears in a migration, this stops being a commerce tool and becomes a treatment app:
FTC/FDA exposure, processor risk, and a materially different insurance conversation."

A rule this consequential cannot depend on every reviewer remembering it. Spec §10
lists, as priority test coverage, "a schema test that fails the build if a forbidden
column name appears — cheap insurance on §2."

## Decision

`internal/compliance` scans the migrations, the generated models, and the API DTOs for
identifiers matching a forbidden set (`doses_per_day`, `servings_per_day`,
`units_per_day`, `doses_remaining`, `adherence`, `intake`, `symptom`, `usage_rate`,
`supply_remaining`, and relatives). A match fails the test, and therefore the build,
and therefore the PR.

It runs as part of `make test`, so it gates every pull request through the normal
`test` check — no separate workflow, nothing to configure in branch protection.

## Consequences

- The boundary is enforced by the build rather than by reviewer memory. Spec §2 asks
  for exactly this: "Enforce it at the schema level and reject PRs that add such a
  column."
- **The guard must never be weakened to make a change pass.** If it fires, the field
  is renamed or the feature is reconsidered. `CLAUDE.md` states this as a rule.
- False positives are possible and acceptable — a legitimate field caught by the
  pattern is a cheap conversation, while a missed one is a regulatory problem.
- The guard covers identifiers, not customer-facing prose. Copy review
  ("when to reorder," never "when to take") remains a human responsibility on emails,
  portal strings, and error messages, and is listed as a criterion in
  `.claude/agents/security-reviewer.md`.
- A guard that has never failed is not known to work. Its own test asserts that a
  planted forbidden identifier is actually caught.
