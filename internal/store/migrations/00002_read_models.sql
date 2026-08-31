-- +goose Up
-- +goose StatementBegin

-- Spec §8: "Expose as read-model views, not by querying the write tables
-- directly." These project off the write tables from migration 00001 — schedules,
-- schedule_items, occurrences, schedule_events — the same append-only log spec §3
-- calls the CQRS/event-sourcing seam.
--
-- Two of spec §8's five outputs have a real gap against this schema and are
-- deliberately not built as originally worded. See docs/adr/0006 for why:
--   - "Predicted revenue" needs a unit price, which this service does not store by
--     design (spec §1: WooCommerce owns catalog). v_occurrence_forecast reports
--     occurrence and unit counts instead; dollars wait for Phase 2's catalog feed.
--   - "Cohort retention by ... acquisition source" has no acquisition-source column
--     and no writer yet — spec §9 implies it arrives with the originating
--     WooCommerce order. v_cohort_retention covers SKU and cadence only.

-- Cadence distribution per SKU (spec §8). This is the empirical answer spec §11
-- decision #4 asks for, and needs nothing beyond Phase 1's schema: every schedule
-- has carried interval_days and its items since the first migration. status is
-- included in the grouping, not filtered out, so a consumer can compare the
-- cadence mix of active schedules against churned ones — a schedule canceled for
-- "too_frequent" is itself a signal about that cadence choice.
CREATE VIEW v_cadence_distribution AS
SELECT
    si.sku,
    s.interval_days,
    s.status,
    count(DISTINCT s.id) AS schedule_count
FROM schedule_items si
JOIN schedules s ON s.id = si.schedule_id
GROUP BY si.sku, s.interval_days, s.status;

-- Churn reason codes (spec §8), sourced from the reason_code the cancel transition
-- already writes onto schedule_events (internal/schedule/service.go Cancel()).
--
-- The seven codes are hardcoded here as they are in domain.CancellationReasons
-- (internal/domain/transitions.go) — the same coupling the schedule_status and
-- occurrence_status enum types already have to their Go constants. Adding a
-- reason code needs both a Go change and a new migration to add it here.
--
-- LEFT JOINed against the closed set so a code with zero cancellations reports a
-- row with count 0, not silence — a dashboard reading this view should never have
-- to guess whether an absent code means "never happened" or "the view doesn't
-- know about it."
CREATE VIEW v_churn_reasons AS
SELECT
    reasons.reason_code,
    count(e.id) AS cancellation_count,
    min(e.created_at) AS first_at,
    max(e.created_at) AS last_at
FROM (VALUES
    ('too_expensive'),
    ('too_frequent'),
    ('switched_brand'),
    ('delivery_issue'),
    ('payment_issue'),
    ('no_longer_wanted'),
    ('other')
) AS reasons(reason_code)
LEFT JOIN schedule_events e
    ON e.reason_code = reasons.reason_code
    AND e.event_type = 'schedule.canceled'
GROUP BY reasons.reason_code;

-- Occurrence/unit-count forecast (spec §8, scoped down from "predicted revenue" —
-- see the module comment above and docs/adr/0006). Only 'planned' occurrences are
-- counted; 'pending' should be added here once Phase 2's Arm step (spec §5) can
-- produce it, since an armed occurrence is an equally real forward commitment.
--
-- Joining occurrences to the schedule's current item list, rather than a
-- per-occurrence item snapshot, is deliberate: schedule_items has no history, so
-- this reports what will ship under the schedule's *current* configuration — the
-- correct question for a forward-looking forecast, and consistent with how
-- change_cadence and the other transitions already treat "current" as the only
-- state that matters going forward.
CREATE VIEW v_occurrence_forecast AS
SELECT
    date_trunc('week', o.scheduled_for::timestamp)::date AS week_start,
    si.sku,
    count(DISTINCT o.id) AS occurrence_count,
    sum(si.quantity) AS unit_count
FROM occurrences o
JOIN schedules s ON s.id = o.schedule_id
JOIN schedule_items si ON si.schedule_id = s.id
WHERE o.status = 'planned'
GROUP BY 1, 2;

-- Audience segments (spec §8): "paused, failed, canceled-within-90d are three of
-- the highest-intent lists the portfolio will ever have."
--
-- segment_since is read from schedule_events, not schedules.updated_at: the event
-- log is the audit trail (spec §3), and an unrelated column update must not be
-- able to shift when a segment "started". The 'failed' branch has no event type
-- to read yet — nothing produces that status until Phase 4's dunning ladder adds
-- one — so segment_since is left NULL there rather than reaching for updated_at
-- as a stand-in, which would misrepresent it as event-sourced when it isn't.
CREATE VIEW v_audience_segments AS
SELECT
    s.customer_id,
    s.id AS schedule_id,
    'paused'::text AS segment,
    (SELECT max(e.created_at) FROM schedule_events e
        WHERE e.schedule_id = s.id AND e.event_type = 'schedule.paused') AS segment_since
FROM schedules s
WHERE s.status = 'paused'

UNION ALL

SELECT
    s.customer_id,
    s.id,
    'canceled_within_90d'::text,
    (SELECT max(e.created_at) FROM schedule_events e
        WHERE e.schedule_id = s.id AND e.event_type = 'schedule.canceled')
FROM schedules s
WHERE s.status = 'canceled'
  AND EXISTS (
    SELECT 1 FROM schedule_events e
    WHERE e.schedule_id = s.id
      AND e.event_type = 'schedule.canceled'
      AND e.created_at > now() - interval '90 days'
  )

UNION ALL

SELECT
    s.customer_id,
    s.id,
    'failed'::text,
    NULL::timestamptz
FROM schedules s
WHERE s.status = 'failed';

-- Cohort retention by SKU and cadence (spec §8), signup-month cohorts. Acquisition
-- source is out of scope — see the module comment above.
CREATE VIEW v_cohort_retention AS
SELECT
    date_trunc('month', s.created_at)::date AS cohort_month,
    s.interval_days,
    s.status,
    count(*) AS schedule_count
FROM schedules s
GROUP BY 1, 2, 3;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP VIEW IF EXISTS v_cohort_retention;
DROP VIEW IF EXISTS v_audience_segments;
DROP VIEW IF EXISTS v_occurrence_forecast;
DROP VIEW IF EXISTS v_churn_reasons;
DROP VIEW IF EXISTS v_cadence_distribution;
-- +goose StatementEnd
