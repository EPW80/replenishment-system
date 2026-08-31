# Replenishment Service — Technical Spec v0.1

**Working name:** CadenceOS
**Owner:** Erik Williams
**Status:** Draft — open decisions listed in §11
**First deployment target:** House of Helix (houseofhelixpeptides.com)

---

## 1. Purpose

A standalone scheduling service that turns one-time WooCommerce purchases into recurring orders on a customer-controlled cadence, with pause, skip, defer, and cadence-change built in.

It is deliberately **not** a WooCommerce Subscriptions configuration. The service owns schedules; WooCommerce owns catalog, checkout, payment, and fulfillment. That split is what makes the same service deployable behind Advanced Sequence, Five Minute Tees, and any future node without a rewrite — the portfolio mandate from the AI Growth OS deck ("build once, reuse across brands").

Architecturally this is PartnerOS's sibling: Go service, Postgres, durable queue, nightly reconciliation, HTTP client against a third-party API with error classification. Much of PartnerOS's scaffolding transfers directly.

---

## 2. Scope

### In scope

- Per-customer, per-line-item replenishment schedules
- Cadence expressed in **days between shipments**, nothing else
- Pause / resume / skip-next / defer-by-N-days / change-cadence / cancel
- Order materialization into WooCommerce via REST API
- Payment retry ladder (dunning) on failure
- Lifecycle email via Postmark
- Customer-facing self-service portal (headless, embedded in the WP theme)
- Admin views: upcoming queue, failure queue, churn reasons

### Out of scope

- Anything that reads as clinical guidance. **Hard constraint, not a preference.**

### The compliance boundary (read before designing any schema)

The service stores `interval_days` and nothing that implies consumption. It must never store, compute, infer, or display:

- units-per-day, servings-per-day, or any usage rate
- doses remaining, "you have 4 days left," or supply-depletion projections
- adherence streaks, missed-dose reminders, or intake logging
- outcome tracking, symptom logs, or goal progress
- any per-compound cadence *recommendation*, whether authored or model-generated

A default cadence per SKU is fine — it is a merchandising decision derived from pack size and observed reorder behavior, and it should be documented as such in the SKU config, in those words. The instant a field named `doses_per_day` appears in a migration, this stops being a commerce tool and becomes a treatment app: FTC/FDA exposure, processor risk, and a materially different insurance conversation. Enforce it at the schema level and reject PRs that add such a column.

Copy rule for every surface: **"when to reorder," never "when to take."**

---

## 3. Domain model

```
Customer         1 ─── n  Schedule
Schedule         1 ─── n  ScheduleItem      (SKU + quantity)
Schedule         1 ─── n  Occurrence        (a single planned/attempted fulfillment)
Occurrence       0 ─── 1  Order             (WooCommerce order ref, once placed)
Occurrence       1 ─── n  Attempt           (payment/order-creation attempts)
Schedule         1 ─── n  ScheduleEvent     (append-only audit log)
```

### Schedule

| field | type | notes |
|---|---|---|
| `id` | uuid | |
| `customer_id` | text | WooCommerce customer ID |
| `status` | enum | `active`, `paused`, `canceled`, `failed` |
| `interval_days` | int | 7–180, validated |
| `anchor_date` | date | schedule origin, for drift-free calculation |
| `next_run_date` | date | materialized, indexed |
| `timezone` | text | IANA, from customer profile |
| `payment_token_ref` | text | opaque gateway vault reference — never card data |
| `shipping_address_id` | text | WooCommerce address ref |
| `discount_pct` | numeric | replenishment incentive, see §7 |
| `paused_until` | date | null unless `paused` |
| `created_at` / `updated_at` | timestamptz | |

`next_run_date` is always recomputed from `anchor_date + (n × interval_days)`, never from `last_run + interval`. Incremental addition accumulates drift across skips, deferrals, and retries; anchor-relative computation does not.

### Occurrence

The unit of work. Materialized ahead of time (§5) so the customer portal can show a real upcoming queue and so skips/defers have something concrete to act on.

| field | type | notes |
|---|---|---|
| `id` | uuid | |
| `schedule_id` | uuid | |
| `sequence_no` | int | nth occurrence for this schedule |
| `scheduled_for` | date | |
| `status` | enum | `planned`, `pending`, `placed`, `skipped`, `failed`, `canceled` |
| `order_id` | text | WooCommerce order, null until placed |
| `idempotency_key` | text | `schedule_id:sequence_no`, unique |

The idempotency key is the whole safety story for order creation. A retry, a duplicate queue delivery, or a redeployment mid-run must never produce a second charge. Unique constraint in Postgres, plus the same key passed to the gateway.

### ScheduleEvent

Append-only. Every state transition, with actor (`customer`, `admin`, `system`), reason code, and payload diff. This is the CQRS/event-sourcing seam — the read models in §8 project off this table, and churn analysis needs the reason codes.

---

## 4. Architecture

```
┌────────────────┐      ┌──────────────────┐      ┌─────────────────┐
│ WP theme       │ ───► │ CadenceOS (Go)   │ ───► │ WooCommerce API │
│ portal widget  │ ◄─── │  HTTP + worker   │ ◄─── │ (orders, custs) │
└────────────────┘      └──────┬───────────┘      └─────────────────┘
                               │
                    ┌──────────┼──────────┐
                    ▼          ▼          ▼
               Postgres    Durable Q   Postmark
                           (river/     (lifecycle
                            asynq)      email)
```

- **Go service**, deployed on Hetzner via Coolify alongside PartnerOS.
- **Thin WP mu-plugin** exposes the portal and proxies authenticated calls. No business logic in PHP — it holds a nonce-to-JWT exchange and nothing else.
- **Durable queue** for occurrence execution. Reuse whatever PartnerOS settled on rather than introducing a second queue library.
- **Nightly reconciliation job**, mirroring PartnerOS's nightly approval pass: re-materialize horizons, detect orphaned occurrences, alert on schedules whose `next_run_date` is in the past.

### Why not WooCommerce Subscriptions

Three reasons, in order of weight: schedules become portable across brands that may not all run WooCommerce; the pause/skip/defer semantics below are richer than the plugin's; and PHP cron is not a scheduler you want owning revenue. The cost is that renewal orders arrive via REST rather than natively, so subscription-aware reporting plugins won't see them.

---

## 5. Occurrence materialization

A background job maintains a rolling horizon of `planned` occurrences per active schedule — default 3 ahead, configurable.

1. **Materialize** (nightly): for each active schedule, ensure 3 future `planned` occurrences exist.
2. **Arm** (T-72h): transition to `pending`, send the pre-billing notice (§7). This window is the legal and UX requirement — the customer must be able to skip before being charged.
3. **Execute** (T-0, in customer timezone, batched off-peak): create the WooCommerce order with the idempotency key, charge via vaulted token, transition to `placed` or `failed`.
4. **Reconcile** (nightly): sweep for occurrences stuck in `pending` past their date.

Cadence changes rewrite unexecuted `planned` occurrences and leave `pending` ones alone unless the customer explicitly skips.

---

## 6. State transitions

| Action | Precondition | Effect |
|---|---|---|
| `pause` | `active` | status → `paused`, optional `paused_until`; unexecuted occurrences → `canceled` |
| `resume` | `paused` | status → `active`; re-anchor to today; re-materialize |
| `skip_next` | next occurrence `planned`/`pending` | that occurrence → `skipped`; subsequent ones unchanged |
| `defer` | next occurrence not yet executed | shift `scheduled_for` by N days; **does not** shift the anchor |
| `change_cadence` | `active`/`paused` | update `interval_days`, re-anchor to last placed order, re-materialize |
| `cancel` | any non-canceled | status → `canceled`; all unexecuted → `canceled`; capture reason code |

`defer` shifting the occurrence but not the anchor is the deliberate choice: a customer who pushes one shipment a week out returns to their normal rhythm afterward rather than permanently sliding. If support reports confusion, revisit — but start here.

---

## 7. Dunning and lifecycle email

All via Postmark, same client pattern as PartnerOS. Templates live in the repo, not the Postmark UI.

**Retry ladder on payment failure:** T+0 → T+3d → T+7d. Three failures moves the schedule to `failed` and notifies the customer with a one-click reactivation link. Never silently cancel; a `failed` schedule is a recoverable asset and a churn signal worth measuring.

**Transactional sends:**

| Trigger | Timing | Content |
|---|---|---|
| Schedule created | immediate | cadence, next date, how to change it |
| Pre-billing notice | T-72h | what's shipping, when charged, skip/defer links |
| Order placed | on success | order confirmation, tracking to follow |
| Payment failed | each rung | update-payment link |
| Schedule failed | after rung 3 | reactivation link |
| Paused / resumed / canceled | immediate | confirmation + reversal path |

Every one of these is a commercial notice. None mentions consumption, timing of use, or a reason the customer might want the product now.

**Replenishment discount:** a percentage applied at order creation, stored per schedule so grandfathering is possible. Decide the number before launch (§11) — it interacts directly with affiliate commission math.

---

## 8. Data out

The point of building this rather than configuring a plugin is that the event log feeds the wider system:

- **Cohort retention** by SKU, cadence, and acquisition source
- **Cadence distribution** per SKU — the empirical answer to what the default should be
- **Churn reason codes** from cancellations
- **Predicted revenue** from the materialized occurrence horizon, which is a genuinely useful forward number rather than a trailing average
- **Audience segments** exported to retargeting: `paused`, `failed`, `canceled-within-90d` are three of the highest-intent lists the portfolio will ever have

Expose as read-model views, not by querying the write tables directly.

---

## 9. PartnerOS interaction

Recurring orders raise the attribution question directly: does the affiliate earn on rebills, and for how long?

CadenceOS's obligation is to emit enough context for PartnerOS to decide — the originating order ID, the affiliate ref captured at first purchase, and the occurrence sequence number — and then stay out of the policy. Commission logic stays in PartnerOS.

Note the known Advanced Sequence hazards apply here too: LiteSpeed JS reordering and the age gate stripping `?ref=` params both threaten first-touch capture, and a rebill inherits whatever attribution the original order got. If first-touch is lossy, recurring revenue quietly inherits the loss and compounds it.

---

## 10. Build phases

**Phase 1 — Core scheduling.** Schema, migrations, schedule/occurrence CRUD, materialization job, anchor-date math. No orders placed. Tests carry the phase.

**Phase 2 — WooCommerce integration.** REST client with error classification and pagination (port PartnerOS's), order creation with idempotency, payment via vaulted token. Behind a feature flag, one internal test customer.

**Phase 3 — State machine.** Pause, resume, skip, defer, change cadence, cancel. Event log. Property-based tests over transition sequences.

**Phase 4 — Notifications.** Postmark templates, pre-billing notice, dunning ladder.

**Phase 5 — Customer portal.** Headless widget in the WP theme. Auth via nonce-to-JWT exchange.

**Phase 6 — Admin + reconciliation.** Upcoming queue, failure queue, nightly sweep, alerting.

**Phase 7 — Read models + export.** Cohort views, churn codes, segment export.

Phases 1–3 are the risky half; 4–7 are mostly mechanical.

### Test strategy

TDD throughout, matching PartnerOS. Priority coverage:

- Anchor-date arithmetic across DST boundaries, leap days, and month-end
- Idempotency: same key twice produces one order, always
- Every state-transition pair, including the invalid ones
- Dunning ladder timing
- WooCommerce client error classification (retryable vs terminal vs auth)
- A schema test that fails the build if a forbidden column name appears — cheap insurance on §2

---

## 11. Open decisions

Stakeholder input needed before Phase 2:

1. **Payment vaulting.** Which gateway holds the token? SeamlessChex's ACH bounce risk is worse for recurring than one-time — a bounce three days post-fulfillment on a repeating charge is a materially different problem.
2. **Replenishment discount.** Percentage, and whether it stacks with affiliate commission or is netted against it.
3. **Affiliate commission on rebills.** Duration and rate. Blocks §9.
4. **Default cadence per SKU.** Seed values, to be replaced by observed data after ~90 days.
5. **Pre-billing window.** 72h assumed; confirm against fulfillment lead time.
6. **Failed-schedule retention.** How long a `failed` schedule stays reactivatable before it's archived.
7. **Address changes mid-schedule.** Does the portal allow it, or does it route to support?

---

## 12. Portability notes

To deploy behind a second brand, what should change is configuration only: catalog source credentials, SKU defaults, email templates, discount policy, theme tokens for the portal. If a second deployment requires touching the state machine or the occurrence model, the abstraction was drawn in the wrong place — treat that as a design bug rather than working around it.
