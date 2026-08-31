# 0001 — A Go service, not a WooCommerce Subscriptions configuration

**Status:** Accepted

## Context

The requirement is recurring orders on a customer-controlled cadence for a WooCommerce
storefront. WooCommerce Subscriptions is the obvious off-the-shelf answer, and
rejecting it needs a reason.

## Decision

Build a standalone Go service that owns schedules and drives WooCommerce through its
REST API. WooCommerce keeps catalog, checkout, payment, and fulfillment.

Three reasons, in order of weight (spec §4):

1. **Portability.** Schedules become portable across brands that may not all run
   WooCommerce. The portfolio mandate is build-once-reuse-across-brands.
2. **Richer semantics.** The pause / resume / skip-next / defer-by-N / change-cadence
   / cancel model in spec §6 is richer than the plugin's.
3. **PHP cron is not a scheduler you want owning revenue.**

## Consequences

- Renewal orders arrive via REST rather than natively, so subscription-aware reporting
  plugins will not see them. Accepted cost.
- The service must own idempotency itself, since it is creating orders remotely. This
  is why `occurrences.idempotency_key` carries a unique constraint (spec §3).
- The event log becomes an asset rather than plugin-internal state — it feeds the
  cohort, churn, and forward-revenue read models in spec §8.
- Architecturally this is PartnerOS's sibling: Go, Postgres, durable queue, nightly
  reconciliation, HTTP client against a third-party API with error classification.
  Much of PartnerOS's scaffolding should transfer.
