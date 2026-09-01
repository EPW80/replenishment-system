# Release metadata

A release metadata record describes one release candidate and the gates it
passed. It is the document an executive approval attaches to.

- Schema: [`schemas/release-metadata.schema.json`](schemas/release-metadata.schema.json)
- Example: [`schemas/release-metadata.example.json`](schemas/release-metadata.example.json)
- Authoring skill: [`.claude/skills/release-metadata/SKILL.md`](../.claude/skills/release-metadata/SKILL.md)
- Pre-promotion audit: [`.claude/agents/release-auditor.md`](../.claude/agents/release-auditor.md)

---

## Why the commit SHA is immutable

An approval is only meaningful if it refers to a specific version of the code.

The failure this design prevents: an executive reviews a change and approves it.
Between approval and deploy, the branch advances — a fix is pushed, a PR is
amended, a rebase rewrites history. The deploy runs against "the branch," which
now contains code nobody approved. The audit trail still shows an approval, so
nothing looks wrong. The approval has quietly become a rubber stamp on unknown
code.

Pinning the record to a full commit SHA removes that gap. The SHA is a hash of
the tree, so it cannot be pointed at different content. Approval of a SHA is
approval of exactly those bytes.

### How immutability is enforced

Four mechanisms, each independently sufficient to catch a drift:

1. **The filename encodes the SHA.** Records live at
   `releases/<commit_sha>.json`. Different code produces a different SHA, so it
   is a different file. There is no path by which amended code inherits an
   existing approval.

2. **Records are append-only.** A record is written once and never edited.
   Editing one retroactively changes what was approved. Enforced by review:
   `/releases/` is owned in `CODEOWNERS` (once operational), and a PR that modifies an existing
   record should be rejected outright rather than re-reviewed.

3. **Full SHAs only.** The schema requires `^[0-9a-f]{40}$`. Branch names and
   tags are rejected because they move. Short SHAs are rejected because they can
   collide and because they invite pasting the wrong one.

4. **The deploy refuses a mismatch.** `production-deploy.yml` takes both
   `commit-sha` and `approved-sha` and fails the job if they differ. This is the
   backstop: even if a wrong SHA reaches the deploy, the run stops before any
   step touches production.

   What this does **not** do: it compares two values handed to the run, so it
   cannot catch an operator who enters the same wrong SHA twice. `approved-sha`
   must be read from the release record rather than retyped. Mechanism 4 catches
   accidents; the Environment reviewer is what stands between a deliberate act
   and production.

### Correcting a record

You do not. A record with a wrong value stays as it is — it is evidence of what
was believed at the time.

If the code needs to change, the new commit has a new SHA and gets a new record,
which goes through the gates again. If a record contains a factual error about an
unchanged commit, write a new record for the same SHA in a separate file
(`releases/<commit_sha>.corrected-<n>.json`) and reference the original. Never
overwrite. The original's existence is the audit trail.

---

## Fields

| Field | Type | Notes |
| --- | --- | --- |
| `project` | string | Human-readable project name. |
| `repository` | string | `owner/repo`. |
| `pull_request` | integer | PR number that introduced the commit. |
| `commit_sha` | string | Full 40-char lowercase hex. **Immutable.** |
| `developer` | string | GitHub login of the author. |
| `human_reviewer` | string | GitHub login of the approving code owner. Must differ from `developer`. |
| `test_status` | enum | `passed` / `failed` / `pending`. From the check-run conclusion. |
| `security_review_status` | enum | `passed` / `failed` / `pending` / `waived`. |
| `staging_url` | string\|null | Where this SHA was verified. `null` means not yet staged. |
| `business_approval_required` | boolean | Whether executive/business sign-off is needed. |
| `rollback_safe` | boolean | Whether the previous version can be redeployed safely. |
| `database_migration` | boolean | Whether schema or stored data changes. |
| `created_at` | string | RFC 3339 timestamp of the record, not of the commit. |

Three further fields apply **only** when `security_review_status` is `waived`.
They are required in that case and forbidden otherwise:

| Field | Type | Notes |
| --- | --- | --- |
| `waiver_authorized_by` | string | GitHub login of the Technical Admin who authorized the waiver. |
| `waiver_reason` | string | Why, and what compensating control applies. Minimum 20 characters. |
| `waiver_timestamp` | string | RFC 3339 timestamp of when the waiver was granted. |

No other fields are permitted — the schema sets `additionalProperties: false`.
Keeping the record narrow is what makes it reviewable at approval time.

### On `test_status`

Set it from the conclusion of the `test` check run for that SHA. It is not a
claim the author makes; it is a fact about a CI run. This is why `test.yml`
exposes no self-reported status output — a failing job cannot be trusted to
report its own result.

### On `security_review_status` and waivers

Combines the `security-check` workflow result with the AI security review.

`waived` remains an allowed state, because a blanket ban on exceptions just
produces dishonest records — someone marks a release `passed` to get it moving.
The design goal is not to prevent waivers but to make them **impossible to record
quietly**.

A waiver must therefore carry all three of:

| Field | Answers |
| --- | --- |
| `waiver_authorized_by` | *who* accepted the risk |
| `waiver_reason` | *why*, and what compensating control applies |
| `waiver_timestamp` | *when* the exception was granted |

The schema enforces this conditionally: set `security_review_status: "waived"` and
all three become required. Set anything else and all three are **forbidden**, so a
record cannot carry half a waiver, and waiver fields cannot linger next to a
`passed` status where they would misrepresent what happened.

`waiver_reason` has a 20-character minimum. That is deliberately blunt: it exists
to reject `n/a` and `ok`, which are the realistic failure mode. Length is not
substance, so the reason is still reviewed by a human and by
[`release-auditor`](../.claude/agents/release-auditor.md).

#### Who may authorize a waiver

**Only a Technical Admin.** No other role may waive the security gate, regardless
of release urgency.

The schema **records** the decision; it does **not** enforce the authorization. It
has no way to know which logins hold the Technical Admin role, so nothing stops a
well-formed record naming someone who is not one. That check is human, and it
belongs in the release audit before promotion.

If you want this enforced mechanically rather than by convention, the enforcement
point is a required reviewer on the `production` Environment restricted to the
Technical Admin group — the same mechanism as the business-approval gate. Decide
that deliberately; it is not configured by this template.

### On `business_approval_required`

The business-approval gate. Set it `true` for anything customer-visible,
contractual, pricing-related, or otherwise beyond a routine technical change.

The field records that approval is required; it does not enforce it. Enforcement
is the required-reviewer rule on the `production` GitHub Environment, which holds
the deploy run until a reviewer approves in the GitHub UI. A boolean in a JSON
file blocks nothing on its own — keep the Environment rule configured.

### On `rollback_safe` and `database_migration`

These two carry the most operational risk, because `rollback.yml` reads
`rollback_safe` and refuses to run when it is `false`.

The combinations:

| `database_migration` | `rollback_safe` | Meaning |
| --- | --- | --- |
| `false` | `true` | Code-only change. Redeploy the previous version freely. |
| `true` | `true` | Additive, backward-compatible migration — old code still runs against the new schema. |
| `true` | `false` | Destructive or non-backward-compatible migration. Recovery is a human-planned operation. |
| `false` | `false` | Unusual but valid: a one-way external side effect, e.g. an irreversible third-party call. Explain it in the PR. |

**`rollback_safe` defaults to `false`.** The schema declares that default, the
example records use it, and `rollback.yml`'s dispatch checkbox starts unticked.
Rollback is never assumed safe until a project's actual deployment implementation
demonstrably supports it — until then, the honest value is `false`.

Do not flip it to `true` because nothing looked alarming. The question is specific:
if the previous version were redeployed unchanged right now, would it run correctly
against the state this release leaves behind?

An incorrect `true` is the dangerous direction — it sends an operator into an
automated rollback that can lose data. An incorrect `false` only costs a
conversation.

---

## No second reviewer, no record

`CODEOWNERS` is not yet operational in this repository (see
[`LIFECYCLE.md`](LIFECYCLE.md) step 6), so peer approval currently depends on people
rather than GitHub. Two merges have already gone through under that gap — PRs #2 and
#5 — with only their author's approval.

`human_reviewer` must differ from `developer`. A solo-maintainer merge has no second
human to name there, and the fix is not to relax that requirement. **A merge with no
real second human reviewer does not get a release-metadata record.** Naming the
author as reviewer, or naming a bot, would be exactly the dishonest record the
append-only design in this document exists to prevent.

This is a gap, not a workaround: those commits sit outside the release process until
`CODEOWNERS` (or an equivalent human peer-review step) is operational. Nothing is
backfilled once it is — records simply resume for merges from that point forward.

---

## Storage

```
releases/
  3f7a1c9e5b2d8a4f6e0c1b3d5a7f9e2c4b6d8a0f.json
  8b2e4d6a0c9f1e3b5d7a9c1e3f5b7d9a1c3e5f70.json
```

The directory does not exist in this template — the first release creates it.

Nothing here requires a database. Records are files in git, so they inherit git's
history: who added a record, when, and under which PR. That is the audit trail,
and it is why the append-only rule matters more than any tooling would.

---

## Validating a record

The template ships no validator, because a validator means choosing a language
and this template does not assume one. Options, in rough order of preference:

- **In CI, once the stack exists** — use the JSON Schema validator native to your
  ecosystem, and run it as part of `security-check` or a dedicated job.
- **Locally** — any draft 2020-12 compliant validator (`check-jsonschema`, `ajv`,
  and equivalents in most languages).
- **By review** — the `release-auditor` agent checks the schema and the
  consistency rules the schema cannot express.

Three rules are deliberately outside the schema, because JSON Schema cannot
express any of them:

1. `human_reviewer` must differ from `developer`.
2. A destructive migration must mean `rollback_safe: false`.
3. `waiver_authorized_by` must actually hold the Technical Admin role.

All three are checked by `release-auditor` and by the human reviewer. The schema
can prove a record is *well-formed*; only a person can confirm it is *true*.
