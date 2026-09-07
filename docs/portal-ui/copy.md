# Copy deck

Every user-facing string in the portal and admin screens, and the rule that governs
all of them.

---

## The rule

**"When to reorder," never "when to take."**

This is spec §2 and the compliance boundary in CLAUDE.md, and it is a hard constraint
rather than a house style. It applies identically to response fields, error strings,
email templates, portal copy, admin labels, chart axis titles, and CSV column headers.

The service stores `interval_days` and nothing that implies consumption. So the copy
describes **shipment timing** and never product use:

| Never write | Write instead |
| --- | --- |
| "Your next dose" | "Your next order" |
| "You have 12 days left" | "Charges in 12 days" |
| "Running low?" | "Want it sooner?" |
| "How often do you take it?" | "How often should this repeat?" |
| "Too much left over" | "Orders arrive closer together than I want" |
| "Days of supply" | "Days between orders" |
| "Reminder to take" | "Reminder before we charge" |

Banned outright, in any surface: units or servings per day, doses remaining, supply
projections, adherence streaks, missed-dose language, intake logging, outcome or
symptom tracking, and any per-compound cadence *recommendation* — authored or
generated.

A default cadence per SKU is allowed and is a merchandising decision derived from pack
size and observed reorder behaviour. Say it in those words wherever it is surfaced;
the churn view's annotation is the worked example.

`internal/compliance` fails the build on a forbidden identifier and runs under
`make test`. It scans identifiers only — Go declarations, struct tags, SQL table and
column names — and deliberately not prose, since the text describing the prohibition
has to contain the words it bans ([ADR 0005](../adr/0005-compliance-boundary-enforcement.md)).

Nothing mechanical will catch a violation in a button label. `.claude/agents/security-reviewer.md`
and `.github/PULL_REQUEST_TEMPLATE.md` carry the criterion for review; this file is
what a reviewer checks against.

---

## Strings the domain layer already owns

`internal/domain/transitions.go` carries customer-facing messages for every rejected
transition, written to the copy rule and returned through `TransitionError.CustomerMessage()`.
The handler maps them to **409** with the message in `{"error": "..."}`.

**Render these verbatim.** Do not re-word them in the portal, and do not replace them
with a generic "something went wrong." They are the most precisely-audited strings in
the codebase, and a second wording is a second thing to keep compliant.

| Action | Schedule status | Message |
| --- | --- | --- |
| pause | `paused` | this schedule is already paused |
| pause | `canceled` | this schedule has been canceled |
| pause | `failed` | this schedule needs a payment method update before it can be paused |
| resume | `active` | this schedule is already active |
| resume | `canceled` | this schedule has been canceled and cannot be resumed |
| resume | `failed` | this schedule needs a payment method update before it can be resumed |
| skip | `paused` | this schedule is paused, so no order is scheduled to skip |
| skip | `canceled` | this schedule has been canceled |
| skip | `failed` | this schedule has no upcoming order to skip |
| defer | `paused` | this schedule is paused, so no order is scheduled to move |
| defer | `canceled` | this schedule has been canceled |
| defer | `failed` | this schedule has no upcoming order to move |
| cadence | `canceled` | this schedule has been canceled |
| cadence | `failed` | this schedule needs a payment method update before its cadence can change |
| cancel | `canceled` | this schedule has already been canceled |
| *fallback* | any | this schedule cannot accept that change right now |

And for an action aimed at an occurrence that has already settled:

| Occurrence status | Message |
| --- | --- |
| `placed` (default) | this order has already been placed and can no longer be changed |
| `skipped` | this order has already been skipped |
| `canceled` | this order has already been canceled |
| `failed` | this order could not be placed; update your payment method to restart the schedule |

A 409 is not a failure to apologise for. The request was well-formed and the customer
was allowed to make it; the schedule was simply in a state that could not accept it.
Show the message in place, leave the screen usable, and re-fetch — the most common
cause is a stale view.

---

## Cancellation reason codes

The closed set is `domain.CancellationReasons`. **Send the code; the label is only
ever shown.** A label that reaches `reason_code` is rejected by
`ValidateCancellationReason`, and if it were not, it would silently break the churn
aggregation in spec §8.

| Code | Label shown to the customer |
| --- | --- |
| `too_frequent` | Orders arrive closer together than I want |
| `too_expensive` | It costs more than I want to spend |
| `no_longer_wanted` | I do not want this product any more |
| `switched_brand` | I am buying this somewhere else |
| `delivery_issue` | There was a problem with delivery |
| `payment_issue` | There was a problem with payment |
| `other` | Something else |

`too_frequent` is deliberately about **arrival spacing**, not about having too much of
the product. That distinction is the copy rule applied to the single string most likely
to slip across it.

---

## Status labels

| Value | Portal label | Admin label |
| --- | --- | --- |
| schedule `active` | Active | Active |
| schedule `paused` | Paused | Paused |
| schedule `canceled` | Cancelled | Canceled |
| schedule `failed` | Needs payment update | Failed |
| occurrence `planned` | Scheduled | Planned |
| occurrence `pending` | Charging soon | Armed |
| occurrence `placed` | Placed | Placed |
| occurrence `skipped` | Skipped by you | Skipped |
| occurrence `failed` | Could not be charged | Failed |
| occurrence `canceled` | — (hidden) | Canceled |

Two notes. The portal says "Needs payment update" where the API says `failed`, because
`failed` reads to a customer as something they broke; spec §7 is explicit that a failed
schedule is a recoverable asset, and the label should say so. And the enum spellings
stay American (`canceled`) because that is what the database holds — only the portal
label is localised.

Every status pill pairs its color with a text label. Color alone never carries state.

---

## Screen copy

Strings that are not error messages. Bracketed values are placeholders for facts that
are not settled — see the table at the end of [`screens.md`](screens.md).

### Active schedule

- Eyebrow: `[Brand] · Account`
- Heading: `Recurring orders`
- Intro: `Reorder on a schedule you set. Change how often it repeats, skip an order, or push one back — any time up to 72 hours before it is charged.`
- Field labels: `Next order`, `Interval`, `Started`, `Time zone`, `Ships to`, `Payment`, `Recurring discount`
- Interval value: `Every {n} days`
- Countdown: `Repeats every {n} days · {d} days from now`
- Items header: `What ships each time` / caption `Names and prices from the store catalog`
- Queue header: `Upcoming orders` / caption `Three scheduled ahead`
- Queue note: `Skip or push back any order up to 72 hours before it is charged. We email you before each one.`
- Past header: `Already sent`
- Buttons: `Change interval`, `Pause`, `Skip`, `Push back`
- Footer: `Questions? Contact [support email].` / `Cancel recurring orders`

### Pre-billing window

- Band: `Your next order is charged in {d} days`
- Band body: `We will charge your card on file and place the order on {date}. Skip it or push it back before {deadline} and nothing is charged.`
- Sheet heading: `Skip the order on {date}?`
- Sheet body: `You will not be charged for this one. Your schedule keeps its rhythm — the next order stays on {date}, exactly {n} days on from the one you are skipping.`
- Nudge: `Want the gap to be longer every time instead? Change the interval rather than skipping repeatedly.`
- Buttons: `Skip this order`, `Keep it`, `Push back`

### Change interval and push back

- Heading: `How often should this repeat?`
- Body: `Pick the gap between orders. Anything from 7 to 180 days. This changes every future order, not just the next one.`
- Preview labels: `Now — every {n} days`, `After — every {n} days`
- Preview note: `Dates count forward from your last order placed on {date}, so the spacing stays exact instead of drifting a little each time.`
- Defer heading: `Push back just this order`
- Defer body: `Move the {date} order later without changing your interval. Afterwards you go straight back to your normal rhythm — this does not slide every order forward.`
- Defer note: `Order after it stays on {date}, unchanged.`
- Buttons: `Save interval`, `Cancel`

### Pause and resume

- Heading: `Pause your recurring orders`
- Body: `Nothing is charged while paused, and the orders already scheduled are cleared. Set a date to start again automatically, or leave it open and resume whenever you like.`
- Option A: `Resume automatically on a date` / `We start your schedule again that morning — no action needed from you.`
- Option B: `Pause until I say otherwise` / `Resume from this page whenever you want to reorder.`
- Paused card: `Resuming {date}` / `Nothing will be charged before then. When it resumes, your first order goes out that day and the {n}-day rhythm starts again from there.`
- Buttons: `Pause orders`, `Never mind`, `Resume now`

### Payment failed

- Band: `We could not charge your card`
- Band body: `We tried three times over the past week for the order due {date}. Your schedule is on hold — update your card and it picks up right where it left off, no orders lost.`
- Buttons: `Update card and restart`, `Not now`

Never "your subscription lapsed" or anything implying the customer lost something.
Spec §7: never silently cancel, and a failed schedule is a recoverable asset.

### Cancel

- Heading: `Cancel your recurring orders?`
- Body: `Nothing further will be charged and your scheduled orders are cleared. You can order again any time from the store — this only ends the schedule.`
- Reason prompt: `Why are you cancelling?` / `Optional, but it genuinely helps us`
- Deflection (`too_frequent` only): `A longer gap might fix that` / `You are on {n} days. Moving to {a} or {b} keeps the recurring discount and stretches out the deliveries.`
- Buttons: `Cancel recurring orders`, `Keep my schedule`, `Go to {n} days`, `Pause instead`

---

## Transactional email

Four templates already exist in `internal/notify/templates/` — created, paused,
resumed, canceled — and they set the voice the portal matches. They are plain,
unstyled, and say only what happened and how to reverse it.

Spec §7 lists three more that are not written: the T−72h pre-billing notice, order
placed, and the dunning rungs. Every one of them is a commercial notice. None mentions
consumption, timing of use, or a reason the customer might want the product now.

The pre-billing notice is the one with legal weight: it is what makes the skip window
real, and its copy should be reviewed alongside whatever the open decision in spec §11.5
settles about the window's length.

---

## Voice

Plain, second person, contractions where they read naturally. State what will happen and
when, then how to change it. No exclamation marks, no emoji, no urgency language, no
"Oops."

The customer is managing a delivery schedule, not being marketed to. The one place the
portal argues a case is the `too_frequent` deflection, and it argues by offering a
longer interval — which is the thing the customer just said they wanted.
