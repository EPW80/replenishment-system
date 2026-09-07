# Components

The inventory behind the nine artboards. Names are the ones used in
[`screens.md`](screens.md); values are the tokens in [`tokens.css`](tokens.css).

The artboards are drawn with inline styles, because that is what the design canvas
edits. Do not port them that way — build these components once and compose the screens
from them. Every screen is made of the pieces below and nothing else.

---

## Portal

### Status pill

Schedule or occurrence state. A 6px dot, a text label, a tinted rounded-full ground.

**Color never carries state alone.** The dot is redundant with the label, deliberately:
this is the accessibility floor for a status indicator, and it also survives the
grayscale printout somebody will inevitably take into a meeting.

| Variant | Dot / text / ground | Used for |
| --- | --- | --- |
| ok | `--cad-ok` / `--cad-ok-text` / `--cad-ok-tint` | active, placed |
| warn | `--cad-warn` / `--cad-warn-text` / `--cad-warn-tint` | pending (armed) |
| crit | `--cad-crit` / `--cad-crit-text` / `--cad-crit-tint` | failed |
| idle | `--cad-idle` / `--cad-idle-text` / `--cad-idle-tint` | planned, paused, skipped |

12px text, 600 weight, 4px/11px padding. The "Placed" and "Skipped" pills in the
already-sent list swap the dot for a 12px check or cross icon.

### Button

44px tall, 4px radius, 600 weight, 14px label. Icon optional at 16px on the left, 8px
gap.

| Variant | Fill / border / text |
| --- | --- |
| primary | `--cad-accent` / `--cad-accent` / white |
| secondary | `--cad-surface` / `--cad-border-strong` / `--cad-ink` |
| danger | `--cad-crit` / `--cad-crit` / white |
| row action | as secondary, 36px tall, 13px label |
| on tinted band | `--cad-surface` / `--cad-accent-edge` or `--cad-warn-edge` |

At most one primary per view. On the cancel screen the primary is the *danger* button
and "Keep my schedule" is secondary — the destructive action is what the customer came
to do, so hiding it behind a secondary treatment is a dark pattern, not politeness.

**States nobody drew.** Hover darkens the fill to `--cad-accent-strong` (or the border
to `--cad-ink-faint` for secondary). Focus takes a 2px `--cad-accent` outline at 2px
offset — required, and the artboards do not show it. Disabled drops to
`--cad-ink-ghost` on `--cad-surface-sunk` with no border change. Pending shows a
spinner in place of the icon and keeps the label; do not swap the label for
"Saving…" — the width change moves everything beside it.

### Choice chip

Interval and defer presets. 44px tall, fully rounded, 14px 600 label.

Unselected is `--cad-surface` on `--cad-border-strong`. Selected is `--cad-accent-tint`
ground, `--cad-accent` border, `--cad-accent-strong` text, with a 14px check icon
appearing to the left of the label.

The check is what makes selection legible without relying on the tint. Reserve its
space in the unselected state so selecting a chip does not reflow the row.

### Card and sheet

`--cad-surface` on a 1px `--cad-border`, 6px radius. Sections inside are separated by a
full-bleed 1px `--cad-border` rule, not by margin. Horizontal padding
`--cad-pad-sheet`; vertical varies by section density.

The already-sent section on the active-schedule card sits on `--cad-surface-row` with
the card's bottom radius, marking it as settled without needing a heading treatment.

### Inset panel

`--cad-surface-sunk` on `--cad-border-hairline`, 4px radius, `--cad-pad-inset`. Carries
consequence copy inside a sheet: the cadence preview, the skip nudge, the cancel
deflection.

### Field

Uppercase 11px `--cad-ink-faint` 600 label with `--cad-eyebrow-tracking`, and a 15px
`--cad-ink` value beneath. Laid out as a 3-column grid on desktop, stacked on phone.

A value that is a placeholder renders in `--cad-ink-muted` rather than `--cad-ink`, so
an unresolved product name reads as pending rather than as content.

### Occurrence row

Sequence number in mono `--cad-ink-ghost`, date and countdown stacked, status pill,
then row actions. Separated by 1px `--cad-border-hairline`, no ground fill.

Reserve the action column's width even on rows that have no actions — the already-sent
rows keep an empty spacer — so the pills stay on one vertical line down the list.

### Notice band

Full-width band at the top of a sheet: 20px icon, heading, body, actions on the right.

| Variant | Ground / border / text | Used for |
| --- | --- | --- |
| warn | `--cad-warn-band` / `--cad-warn-edge` / `--cad-warn-text` | T−72h window |
| crit | `--cad-crit-band` / `--cad-crit-edge` / `--cad-crit-text` | payment failed |

Two bands never stack. If a schedule is both armed and carrying a failure, the failure
wins — it is the one the customer can act on.

### Radio option row

A full row is the hit target, not just the control: 12px radius-4 bordered row, 17–18px
radio, label and helper text stacked, minimum 48px tall. Selected takes
`--cad-accent-tint` ground and `--cad-accent` border with a 5px-ring radio.

Used for pause mode and for the seven cancellation reasons. The reason grid is two
columns on desktop with "Something else" spanning both, single column on phone.

### Stepper

40–44px bordered field with a mono value, a unit label, and stacked increment and
decrement affordances. Clamp to the action's bounds (7–180 for interval, 1–180 for
defer) rather than letting the field reach an invalid value the server will refuse.

---

## Admin

### Sidebar nav item

38px, 4px radius, 16px icon, 14px label. Active takes an `#E9E0D4` ground and 600
weight with the icon in `--cad-accent-strong`. A count badge sits right-aligned as a
rounded-full 11px chip; the failure count uses `--cad-crit-tint` on `--cad-crit-text`.

The rail footer carries the environment name and the last nightly run. That second line
is load-bearing: `docs/SCHEDULED_JOBS.md` says the failure mode is silent and the check
that matters is whether a run has happened at all recently. Putting it in the chrome
means an operator sees it without going to look for it.

### Stat tile

Card, 15px/17px padding, uppercase 11px label, 28–30px `--cad-font-serif` figure. A
12px `--cad-ink-faint` caption underneath where the number needs qualifying.

Four per row maximum. A tile whose number nobody would act on does not belong on the
page — resist filling the row for symmetry.

### Data table

CSS grid, not `<table>`, so column widths are declared once and the rows compose from
components. Header strip on `--cad-surface-row` with uppercase 11px labels; cells at
`--cad-table` with 13–14px vertical padding and a 1px `--cad-border-hairline` bottom.
Last row drops its border; the footer strip carries the count and pagination.

IDs, dates, sequence numbers, error classes and counts are mono. Everything a person
reads as language is sans. The selected row takes a `#FBF6F4` ground across every cell.

### Detail drawer

372px fixed column beside the table: header with ID and status, then stacked sections
divided by `--cad-border` rules, then a vertical stack of actions with the primary
first. Sections are the retry ladder, the event log, and the actions.

### Ladder meter

Three 22×6px rounded segments plus an "N of 3" label. Filled segments are
`--cad-crit`, unfilled are `--cad-border`. The label is what states the position; the
segments are the glance.

Three is the dunning ladder length from spec §7 (T+0, T+3d, T+7d) and is not a
configurable count in this component — if the ladder becomes configurable, this
component changes with it.

### Event log entry

Mono date, mono event type, right-aligned actor. Event types come from the constants in
`internal/domain/schedule.go` (`schedule.paused`, `occurrence.skipped`, …) and are shown
raw, not prettified — an operator reading this is going to grep for the same string.

### Chart marks

One series, so one hue: `--cad-accent`. No legend; the panel heading names the series.

Horizontal bars are 14px tall with a 4px radius on the data end only, anchored to a
shared left edge with the category label right-aligned before it. Columns are
full-width in their grid track with a 4px radius on top, anchored to a 1px
`--cad-border-strong` baseline. Values label the mark directly — mono, 11.5px,
`--cad-ink-faint`, with the mode or leader in `--cad-ink` at 600.

Both charts carry a "View as table" link. The chart is the glance; the table is the
answer to "what exactly."

### Segment chip

36px rounded-full chip pairing a 600 label with a mono count. Read-only — the export
button beside them is the action.

---

## Icons

Inline SVG, stroke-only, 1.6–1.8 stroke width, round caps and joins, on a 24px
viewBox. Rendered at 12px in pills, 14px in chips, 16px in buttons and nav, 20px in
notice bands.

Drawn so far: calendar, pause, refresh, check, cross, clock, warning triangle, info,
chevron down, chevron right, chevron up, search, download, bar chart, list, wave
(the product mark).

No emoji, and no icon-only controls anywhere in the portal — every action carries a
text label.

---

## States the artboards do not show

A mockup shows the happy path. These are the rest, and they are where a build usually
diverges from a design.

**Loading.** Skeleton the card structure rather than showing a spinner over an empty
page: the header block, three field rows, three occurrence rows. The two portal calls
resolve independently, so the header can be live while the queue is still skeletal.

**Empty.** A schedule with no occurrences is not an error — it is a paused schedule, or
one whose horizon has not been materialised yet. Say which: "Paused, so nothing is
scheduled" reads correctly; "No upcoming orders" reads like something broke.

**Error.** A 409 renders inline on the control that caused it, with the message from
the response verbatim (see [`copy.md`](copy.md)) and the screen still usable. A 500
renders as a band on the card with a retry. A 401 means the portal's JWT expired —
re-run the nonce-to-JWT exchange rather than showing an error, and only surface one if
that also fails.

**Offline and in-flight.** Every transition is a POST that changes state a customer
cares about. Disable the control while it is in flight and re-fetch the schedule
afterwards; the response already returns the updated schedule, so use it rather than
optimistically patching local state.

**Idempotency keys.** `skip` and `defer` both require `idempotency_key` in the body,
because both resolve their target occurrence implicitly and a retry has no other way to
say "this is the same request, not a new one" (ADR 0009). Generate the key when the
customer opens the confirmation, not when they submit, and reuse it across every retry
of that one intent. A key generated per-click turns a double-tap into two deferrals.

**Focus and keyboard.** The artboards show no focus states and that is a gap in them,
not a decision. Every interactive element needs a visible focus ring, the reason radio
group needs arrow-key navigation, and the confirmation sheets need focus trapping and
Escape-to-dismiss if they are built as modals.

**Reduced motion.** There is no motion in these designs beyond state transitions. Keep
it that way; if you add any, gate it on `prefers-reduced-motion`.
