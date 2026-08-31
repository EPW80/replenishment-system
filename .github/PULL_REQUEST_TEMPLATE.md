## What and why

<!-- What changes, and why. The diff already says what; explain the reasoning. -->

Closes #

## Risk

These answers become `database_migration`, `rollback_safe`, and
`business_approval_required` in the release metadata record, and those values
determine whether an automated rollback is available later. They are not ceremony —
answer them honestly. See [`docs/RELEASE_METADATA.md`](../blob/main/docs/RELEASE_METADATA.md).

**Database migration** — does this add, alter, or drop schema or stored data?

- [ ] No
- [ ] Yes — and it is backward compatible (old code still runs against the new schema)
- [ ] Yes — and it is **not** backward compatible or is destructive

**Rollback safety** — if the previous version were redeployed unchanged right now,
would it run correctly against the state this release leaves behind?

- [ ] Yes — `rollback_safe: true`
- [ ] No — `rollback_safe: false`

> `rollback_safe` defaults to `false`. Do not flip it to `true` because nothing looked
> alarming. An incorrect `true` sends an operator into an automated rollback that can
> lose data; an incorrect `false` only costs a conversation.

**Business approval** — is this customer-visible, contractual, pricing-related, or
otherwise beyond a routine technical change?

- [ ] No
- [ ] Yes — `business_approval_required: true`

## New secrets or permissions

<!-- Name them. Do not add them silently and do not invent a value. -->

- [ ] None
- [ ] This needs a new secret (named below, value not included)
- [ ] This widens a workflow `permissions:` block — **say so in the PR title**

## Compliance boundary

Spec §2 is a hard constraint, enforced by `internal/compliance` and checked here by a
human for anything the guard cannot see (prose, not identifiers).

- [ ] Stores, computes, infers and displays nothing implying consumption — no usage
      rates, doses remaining, supply projections, adherence tracking, intake logging,
      outcome tracking, or per-compound cadence recommendations.
- [ ] All customer-facing copy says **"when to reorder," never "when to take."**

## Checks

- [ ] `make lint test build security` passes locally
- [ ] Tests cover the change, including the failure cases
- [ ] One PR, one concern
- [ ] No change to `.github/workflows/` bundled with a feature change
