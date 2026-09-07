# Portal and admin UI — developer handoff

Design for the Phase 5 customer portal and the Phase 6 admin views. Nine screens,
their component vocabulary, their tokens, and their copy.

**None of this is built.** The portal is Phase 5 and the admin surface is Phase 6; the
current tree ships the API the portal will call and nothing that renders. Treat this
package as a specification to implement, and read the gaps in
[`screens.md`](screens.md) before estimating anything — several screens depend on
Phase 2 and Phase 4 work that does not exist yet.

## What is here

| File | What it answers |
| --- | --- |
| [`screens.md`](screens.md) | What each screen shows, which endpoint feeds it, what is missing |
| [`components.md`](components.md) | The component inventory, their states, and the states nobody drew |
| [`copy.md`](copy.md) | Every user-facing string, and the compliance rule governing all of them |
| [`tokens.css`](tokens.css) | Resolved design tokens — colors, type, shape, control sizes |
| [`artboards/`](artboards/) | The visual source: one HTML file per screen, plus the canvas layout |

Read them in that order. `screens.md` is the contract; the rest supports it.

## The artboards

`artboards/*.dc.html` are the screens as drawn, and they are the source of truth for
any value this package does not name. They are design-canvas files, not application
code: each one is a standalone static HTML document with inline styles and a
`<script src="./support.js">` line the canvas replaces at render time. Nothing imports
them, nothing builds them, and no exact-value question should be answered by guessing
when the answer is in one of these files.

They are checked in because a handoff whose only visual source is an external link is
not a handoff. `canvas.json` records how they are laid out and grouped into the two
pages, portal and admin.

Do not port their inline styles into components. Build from
[`components.md`](components.md) and [`tokens.css`](tokens.css); read the artboards to
resolve a number.

## Where the design came from

There was no frontend, no stylesheet and no design system in this repository when these
were drawn — the four templates in `internal/notify/templates/` are unstyled HTML. So
the visual language here is new rather than matched, and it is a proposal: warm neutral
ground, one clay accent, Newsreader over Public Sans, mono for anything a customer
might read back to support.

The portal embeds in the WordPress theme (spec §4), so the theme gets a vote on all of
that. What should survive a re-skin is the structure, the states, and the copy; the
palette and the type pairing are the parts spec §12 expects a second brand to override
as configuration.

## The constraint that outranks the design

**"When to reorder," never "when to take."** Spec §2 and the compliance boundary in
CLAUDE.md. It governs every string on every screen, every field name behind them, every
chart axis and every CSV header.

`internal/compliance` fails the build on a forbidden identifier, and it scans
identifiers only — Go declarations, struct tags, SQL table and column names. It
deliberately does not scan prose, for the reason its own package doc gives: the text
describing this prohibition necessarily contains the words it bans, and a guard that
flagged its own documentation would be switched off within a week. See
[ADR 0005](../adr/0005-compliance-boundary-enforcement.md).

So a violation in a button label passes CI. The prose criterion is enforced by review
instead — `.claude/agents/security-reviewer.md` and `.github/PULL_REQUEST_TEMPLATE.md`
both carry it. [`copy.md`](copy.md) is the vocabulary table those reviews should check
against; use it while writing, not after.

The corollary that matters most in practice: `internal/domain/transitions.go` already
owns customer-facing messages for every rejected transition, audited to that rule.
Render them verbatim. Do not paraphrase them in the portal.

## Suggested build order

1. **Tokens and the component set** — pill, button, chip, card, field, occurrence row,
   notice band. Every screen is these seven plus layout.
2. **Active schedule and phone** (screens 1 and 6). Two GETs, no writes. This is the
   screen that proves the data model renders.
3. **Transitions** (screens 3, 4, 5) — cadence, defer, pause, resume, cancel. All six
   endpoints exist and are tested. Get the idempotency-key handling for skip and defer
   right here; see the note in [`components.md`](components.md).
4. **Pre-billing window** (screen 2) — blocked on Phase 2 arming. Build the component,
   drive it from `occurrence.status == "pending"`, expect it to stay dark.
5. **Admin** (screens 7, 8, 9) — blocked on the spec §8 read models, on the Phase 2
   error classification, and on an admin credential that `internal/auth` does not have.
   That auth change wants its own PR ahead of any UI.

Steps 1–3 are buildable today against the API in `internal/httpapi`. Steps 4 and 5 are
not, and no amount of frontend work makes them so.

## Lifecycle

Per CLAUDE.md, work starts from a tracking issue and lands via pull request, one
concern per PR. This package is documentation only: no schema, no workflow, no
`permissions:` block, no release-metadata implications. The implementation it describes
is at least five PRs and should not arrive as one.
