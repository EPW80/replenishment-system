---
name: release-metadata
description: Author an append-only release metadata record for a merged commit. Use when preparing a release, recording gate outcomes, or when asked to "ship" or promote a SHA.
---

# Authoring a release metadata record

A release metadata record describes one release candidate and the gates it passed. It
is the document an executive approval attaches to. Schema:
`docs/schemas/release-metadata.schema.json`. Reasoning: `docs/RELEASE_METADATA.md`.

## Before you write anything

**Records are append-only.** Written once, never edited. If a record already exists
for this SHA, stop — do not modify it. A record with a wrong value stays as it is,
because it is evidence of what was believed at the time. A factual error about an
unchanged commit gets a new file, `releases/<commit_sha>.corrected-<n>.json`,
referencing the original.

**The SHA is the identity.** Write to `releases/<commit_sha>.json`, using the full
40-character lowercase merge commit SHA. Never a branch, never a tag, never a short
SHA — branches and tags move, and short SHAs can collide and invite pasting the wrong
one.

## Gather each value from its source, not from assumption

| Field | Where it comes from |
| --- | --- |
| `project` | `CadenceOS` |
| `repository` | `EPW80/replenishment-system` |
| `pull_request` | The PR number that introduced the commit |
| `commit_sha` | The full merge commit SHA |
| `developer` | GitHub login of the author |
| `human_reviewer` | GitHub login of the approving reviewer. **Must differ from `developer`** |
| `test_status` | **The `test` check-run conclusion for this SHA.** Not a claim you make — a failing job cannot be trusted to report its own result |
| `security_review_status` | The `security-check` result combined with the AI security review |
| `staging_url` | Where this SHA was verified. `null` means not yet staged |
| `business_approval_required` | `true` for anything customer-visible, contractual, pricing-related, or beyond a routine technical change |
| `rollback_safe` | See below — **defaults to `false`** |
| `database_migration` | Whether schema or stored data changes |
| `created_at` | RFC 3339 timestamp of the record, not of the commit |

`additionalProperties` is `false`. No other fields are permitted.

## `rollback_safe` — the one to get right

It defaults to `false`, and `rollback.yml` reads it: `false` refuses to run.

Ask the specific question: **if the previous version were redeployed unchanged right
now, would it run correctly against the state this release leaves behind?** Do not
flip it to `true` because nothing looked alarming. An incorrect `true` sends an
operator into an automated rollback that can lose data; an incorrect `false` only
costs a conversation.

For this service, take particular care when the release touches migrations — a
destructive or non-backward-compatible migration means `rollback_safe: false`, always.

## Waivers

`security_review_status: "waived"` requires all three of `waiver_authorized_by`,
`waiver_reason`, `waiver_timestamp`. Any other status forbids all three.

Only a **Technical Admin** may authorize a waiver. The schema records the decision but
cannot enforce the authorization — so name who authorized it and flag that a human
must confirm the role. `waiver_reason` needs at least 20 characters, but length is not
substance: state why, and what compensating control applies.

## After writing

1. Validate against the schema.
2. Run the `release-auditor` agent (`.claude/agents/release-auditor.md`) — it checks
   the three rules the schema cannot express.
3. **Do not deploy.** Deploys are dispatched by a human, and production additionally
   requires the Environment reviewer. If asked to "ship it," prepare the record and
   report what is still missing.
