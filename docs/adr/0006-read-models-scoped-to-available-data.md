# 0006 — Read models scoped to the data this schema actually has

**Status:** Accepted

## Context

Spec §8 lists five read-model outputs. Building Phase 7 against the real schema
surfaced that two of them are worded for data this service doesn't have and, by
spec §1's own architecture boundary, isn't supposed to have yet:

1. **"Predicted revenue from the materialized occurrence horizon."** Revenue needs a
   unit price. This schema stores no price anywhere — `schedule_items` has `sku` and
   `quantity`, nothing else. That's not an oversight; spec §1 draws the line
   explicitly: "WooCommerce owns catalog, checkout, payment, and fulfillment." A
   price feed is a Phase 2 concern (the WooCommerce REST client).

2. **"Cohort retention by SKU, cadence, and acquisition source."** Acquisition
   source — which affiliate or channel a customer arrived through — has no column
   and no writer. Spec §9 implies it arrives with the originating WooCommerce order
   ("the affiliate ref captured at first purchase"), which again means Phase 2.

## Decision

Build what the schema actually supports today, and name the gap rather than paper
over it:

- `v_occurrence_forecast` reports **occurrence and unit counts**, not dollars. The
  view and the `readmodel.ForecastRow` type are named for what they measure.
- `v_cohort_retention` covers **SKU and cadence only**. No `acquisition_source`
  column was added. An empty column with no writer is exactly the kind of
  speculative field the project's own conventions argue against — see `CLAUDE.md`
  rule against building for hypothetical future requirements, and the discipline
  `internal/compliance` already enforces about not adding fields ahead of a real
  need.

## Consequences

- The dollar figure and the acquisition-source dimension both become natural,
  well-scoped additions once Phase 2 exists: a price feed extends
  `v_occurrence_forecast` with a join, and an `acquisition_source` column on
  `schedules` (written once, at creation, from the originating order) extends
  `v_cohort_retention` with a `GROUP BY`. Neither requires restructuring what Phase 7
  already built.
- Until then, anyone reading `v_occurrence_forecast` or `v_cohort_retention` gets a
  correct, real answer to a narrower question rather than a wrong or fabricated
  answer to the wider one the spec's prose describes.
- `cmd/export`'s CSV headers (`occurrence_count`, `unit_count`, not `revenue`) make
  the scoping visible at the point of use, not just in this document.

## What this does not change

`v_cadence_distribution` and `v_churn_reasons` needed no scoping decision — both are
fully answerable from data Phase 1 and Phase 3 already produce, and are built exactly
as spec §8 describes them.
