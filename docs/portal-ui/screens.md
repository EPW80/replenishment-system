# Screens

Nine screens: six customer portal (Phase 5), three admin (Phase 6). Each entry says
what the screen shows, which endpoint feeds it, which states it has to render, and
what the API does not supply yet.

The artboards in [`artboards/`](artboards/) are the visual source. This file is the
contract between them and the handlers in `internal/httpapi/`.

**Read [`copy.md`](copy.md) before writing any string that reaches a screen.** The
compliance boundary is a build-breaking constraint, not a review preference.

---

## The one thing to get right first

Every screen below is a projection of two API shapes and nothing else:

```
GET /schedules/{id}               -> scheduleResponse
GET /schedules/{id}/occurrences   -> {"occurrences": [occurrenceResponse, ...]}
```

`scheduleResponse` carries `status`, `interval_days`, `anchor_date`, `next_run_date`,
`timezone`, `discount_pct`, `paused_until`, and `items[]` of `{sku, quantity}`.
`occurrenceResponse` carries `sequence_no`, `scheduled_for`, `status`, `order_id`.

That is the whole vocabulary. Anything a screen shows that is not in those two shapes
is either computed client-side from them, fetched from WooCommerce, or listed under
"Not available yet" — there is no third source.

### Four things every screen computes client-side

| Shown | Computed from |
| --- | --- |
| "12 days from now" | `next_run_date` minus today, in the schedule's `timezone` |
| Weekday and long-form date | `scheduled_for`, formatted in the schedule's `timezone` |
| Upcoming vs. already-sent split | `occurrence.status` — see the status table below |
| "Charges in N days" per row | `scheduled_for` minus today |

Do the date arithmetic in `timezone`, not the browser's. A customer in Los Angeles
loading the portal from a hotel in Berlin must see the same date the service will act
on. The service computes `anchor_date + (n × interval_days)`; the portal must never
recompute a date by adding an interval to a date it already rendered, for the same
drift reason recorded in CLAUDE.md and spec §3.

### Occurrence status → where the row goes

`ListOccurrences` returns every occurrence for the schedule, not just future ones, and
it does not paginate. The split is the portal's job.

| `status` | Section | Row actions |
| --- | --- | --- |
| `planned` | Upcoming | Skip, Push back |
| `pending` | Upcoming, armed treatment | Skip, Push back |
| `placed` | Already sent | Link to `order_id` in WooCommerce |
| `skipped` | Already sent | none |
| `failed` | Already sent, critical treatment | Update payment |
| `canceled` | Hidden by default | none |

`canceled` occurrences are the debris of a pause or a cancellation (spec §6). Showing a
customer a list of orders that were removed because they asked for them to be removed
is noise; keep them out of the default view.

---

## Portal

### 1. Active schedule — `Main.dc.html`

The default view. Status header, schedule facts, what ships, the occurrence queue split
into upcoming and already-sent, and the quiet route to cancelling.

| | |
| --- | --- |
| Reads | `GET /schedules/{id}`, `GET /schedules/{id}/occurrences` |
| Renders status | `active` |
| Actions out | Change interval (screen 3), Pause (screen 4), Skip / Push back (row), Cancel (screen 5) |

Two calls, not one. They can go in parallel; render the header as soon as the schedule
resolves rather than blocking the whole page on the occurrence list.

**Not available yet.** Product names — `items[]` carries `sku` and `quantity` only, by
design, because WooCommerce owns the catalog (spec §1). Resolve names through the WP
theme, and treat a name that fails to resolve as non-fatal: the SKU and quantity are
enough to render the row. Order totals are likewise absent from every response; see
"Money" below.

### 2. Pre-billing window — `Armed.dc.html`

The T−72h state: an amber band, the deadline stated in full, and skip and push-back
reachable in one tap. Spec §5 step 2 calls this window the legal and UX requirement,
so it is the screen that most needs to be unmissable rather than tasteful.

| | |
| --- | --- |
| Trigger | the nearest occurrence has `status: "pending"` |
| Reads | same two calls as screen 1 |
| Actions out | `POST /schedules/{id}/skip`, `POST /schedules/{id}/defer` |

**This screen is blocked on Phase 2.** Nothing sets `pending` today — `materialize`
creates `planned` occurrences and stops there; Arm is spec §5 step 2, which
`docs/SCHEDULED_JOBS.md` states plainly is not built. Until arming exists, the portal
has no signal that a charge is imminent and this screen cannot be driven by real data.
Build the component, drive it from `pending`, and expect it to stay dark in Phase 5.

Do not fake the trigger by comparing `scheduled_for` to now. That would put the banner
up on a schedule the service has not actually armed, and a customer who then skips is
skipping something the arming pass would have handled differently.

**Not available yet.** The exact charge cutoff (the artboard says "Wednesday, 16
September at 9:00 AM") is not in any response. The 72-hour window is spec §11 item 5 —
still open, and it needs confirming against fulfilment lead time before this copy can
state a time. Until then, state the date and omit the clock time.

### 3. Change interval and push back — `Cadence.dc.html`

Two timing sheets on one artboard. They are separate actions with different semantics
and must not be merged into one control.

| | |
| --- | --- |
| Change interval | `POST /schedules/{id}/cadence` — `{"interval_days": 42}` |
| Push back | `POST /schedules/{id}/defer` — `{"days": 7, "idempotency_key": "..."}` |
| Bounds | interval 7–180 (`domain.MinIntervalDays`/`MaxIntervalDays`); defer 1–180 (`domain.MaxDeferDays`) |
| Preconditions | cadence: `active` or `paused`; defer: next occurrence not yet executed |

Enforce both bounds in the control, not only on submit. The server validates and
returns 400, but a stepper that lets a customer reach 200 and then refuses them is a
worse experience than one that stops at 180.

**The preview panel is the point of this screen.** Changing cadence re-anchors to the
last placed order and rewrites the unexecuted occurrences (spec §6), so the dates a
customer had memorised all move. Show the before and after date triples, computed the
same way the service will: `last_placed_date + (n × new_interval)`.

**Defer does not move the anchor.** The copy says so, the artboard says so, and the
customer returns to their normal rhythm on the following order. If you find yourself
writing a preview that slides every subsequent date, the implementation has drifted
from spec §6.

### 4. Paused and payment-failed — `Interrupted.dc.html`

Three panels: the pause sheet, the resulting paused card, and the failed-schedule
recovery band.

| | |
| --- | --- |
| Pause | `POST /schedules/{id}/pause` — `{"paused_until": "2026-11-01"}` or empty body |
| Resume | `POST /schedules/{id}/resume` — no body |
| Renders status | `paused`, `failed` |

An empty body is a well-formed pause — `decode` treats `ContentLength == 0` as absent
rather than malformed. Send no body for an open-ended pause; do not send `null` or an
empty string in `paused_until`.

Pause cancels the unexecuted occurrences. The paused card must therefore not show a
stale upcoming queue — re-fetch occurrences after the transition returns, or clear the
list from the client state.

Resume re-anchors to today and re-materialises. So the first order after a resume is
that same day, and the interval restarts from there: say that in the copy, because a
customer who paused expecting to pick up their old dates will otherwise be surprised by
a charge.

**Not available yet.** The failed panel's attempt timeline (three rungs with dates and
decline reasons) has no endpoint. `Attempt` is in spec §3's model and the dunning ladder
is spec §7, both Phase 4. Until then the panel can state that the schedule needs a
payment update — which is exactly what the domain layer's own message says — without
enumerating the attempts.

### 5. Cancel — `Cancel.dc.html`

Reason capture and one honest deflection.

| | |
| --- | --- |
| Cancel | `POST /schedules/{id}/cancel` — `{"reason_code": "too_frequent"}` |
| Codes | the closed set in `domain.CancellationReasons`; a code outside it is rejected |
| Precondition | any status except `canceled` |

The seven labels and their codes are in [`copy.md`](copy.md). Send the code, never the
label: the label is copy that will be rewritten, the code is what spec §8's churn
analysis aggregates, and a label that leaks into `reason_code` breaks the read model
silently.

The deflection panel is shown only for `too_frequent`, and it offers a longer interval
and a pause — both real alternatives that resolve the stated complaint. Do not extend
it into a general retention interstitial: a deflection that has nothing to do with the
reason the customer selected is a dark pattern, and this flow captures the reason
precisely so it does not have to guess.

### 6. Phone — `Mobile.dc.html`

390px. Same data as screen 1, one column, controls at 48px, the manage actions as a
list rather than a button row.

The embedded widget is inside the WP theme, so the phone breakpoint is whatever the
theme's is — treat 390px as the narrow bound to design against, not as a breakpoint to
hardcode.

---

## Admin

### The admin surface does not exist

`NewServiceRouter` has route groups for public, customer, and service credentials, and
a comment saying the Phase 6 admin group is deliberately absent because "a route group
with no routes is clearer than an admin path nobody has designed yet."

These three artboards are that design. They are not built against anything. Everything
below names the read model each view needs, in spec §8's terms; none of those views
exists either. Treat this section as a specification to implement, not as a UI to wire
to endpoints that are waiting for it.

Admin needs a third credential kind. `internal/auth` today mints customer principals
and verifies one service key; an admin acting on a schedule must be recorded as
`domain.ActorAdmin` in the event log, which nothing currently produces. That is an auth
change, and per CLAUDE.md it wants its own PR ahead of any admin UI.

### 7. Upcoming queue — `AdminUpcoming.dc.html`

Four counts and a table of occurrences due, sorted by date.

Needs a read model over `occurrences` joined to `schedules`, filtered to `planned` and
`pending` within a window, with the schedule's interval and customer alongside. Counts:
active schedules, occurrences due in 7 days, occurrences currently armed, and
scheduled value over 30 days — the last of which is spec §8's "predicted revenue from
the materialized occurrence horizon" and is the one number here that is genuinely
forward-looking rather than a trailing average.

### 8. Failure queue — `AdminFailures.dc.html`

The table is failures needing a decision; the drawer is one schedule's ladder, event
log, and recovery actions.

Needs `attempts` (spec §3) with an error classification, which spec §10 assigns to the
Phase 2 WooCommerce client: retryable, terminal, auth. The ladder rendering depends on
knowing which rung an attempt was, and the "Paged on-call" row depends on auth failures
being escalated rather than retried — that is a policy decision nobody has recorded
yet, and it should become an ADR before it becomes a UI affordance.

The event-log strip reads `schedule_events` directly. Note the actor column: it is the
reason the table records an actor at all, and an admin looking at a failed schedule
needs to distinguish a customer who changed their cadence from a system pass that moved
it.

### 9. Churn and cadence — `AdminChurn.dc.html`

Two charts and the segment export strip.

Cancellation reasons is a ranked bar chart over `reason_code` from
`schedule.canceled` events. Cadence distribution is a histogram of `interval_days`
across active schedules — spec §8 calls this "the empirical answer to what the default
should be," and it is the view that eventually settles open decision §11.4.

The segment strip exports `paused`, `failed`, and `canceled-within-90d`. Spec §8 calls
these the three highest-intent lists the portfolio will have. Export is a data egress
path with customer identifiers in it and should not ship without someone deciding where
the file goes and who may ask for it.

Both charts are one series, so one hue and no legend. Keep it that way: a categorical
palette here would imply the reason codes are related to each other, and they are not.

---

## Not available yet — the whole list

Everything the artboards render that no endpoint supplies. Bracketed placeholders in
the artboards mark each one.

| Missing | Needed by | Where it comes from |
| --- | --- | --- |
| Product names | screens 1, 2, 6 | WooCommerce catalog, via the WP theme |
| Order totals and line prices | screens 2, 7, 8 | WooCommerce; nothing in CadenceOS holds money |
| Card summary ("ending 4242") | screens 1, 4 | gateway; `payment_token_ref` is opaque by design |
| Shipping address | screen 1 | WooCommerce; `shipping_address_id` is a reference |
| Recurring discount value | screens 1, 2 | `discount_pct` is in the response, but the *number* is open decision §11.2 |
| Occurrence `pending` state | screen 2 | Arm, spec §5 step 2, Phase 2 |
| Charge cutoff time | screen 2 | pre-billing window, open decision §11.5 |
| Payment attempts and rungs | screens 4, 8 | `attempts`, Phase 2 and 4 |
| Every admin read model | screens 7, 8, 9 | spec §8, Phase 7 |
| Admin credential and actor | screens 7, 8, 9 | `internal/auth`, unbuilt |

### Money

No response in `internal/httpapi` carries a currency amount, and that is not an
oversight to patch in the portal. WooCommerce owns pricing (spec §1), and a total the
portal computes from a cached price is a total that can disagree with the charge. Fetch
it from WooCommerce at render time or leave it out; do not add a price column to
`schedule_items`.
