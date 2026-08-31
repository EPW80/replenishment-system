-- +goose Up
-- +goose StatementBegin

-- Spec §3 domain model. Note what is deliberately absent: this schema stores
-- interval_days and nothing that implies consumption. No usage rate, no doses
-- remaining, no supply projection, no adherence or intake tracking. See spec §2
-- and internal/compliance, which fails the build if such a column appears.

CREATE TYPE schedule_status AS ENUM ('active', 'paused', 'canceled', 'failed');

CREATE TYPE occurrence_status AS ENUM (
    'planned', 'pending', 'placed', 'skipped', 'failed', 'canceled'
);

CREATE TYPE schedule_event_actor AS ENUM ('customer', 'admin', 'system');

CREATE TABLE schedules (
    id                  uuid PRIMARY KEY,
    customer_id         text NOT NULL,              -- WooCommerce customer ID
    status              schedule_status NOT NULL DEFAULT 'active',

    -- Cadence is expressed in days between shipments and nothing else (spec §2).
    interval_days       integer NOT NULL,

    -- Schedule origin. next_run_date is always recomputed as
    -- anchor_date + (n × interval_days), never as last_run + interval:
    -- incremental addition accumulates drift across skips, deferrals and
    -- retries; anchor-relative computation does not. See spec §3, docs/adr/0004.
    anchor_date         date NOT NULL,
    next_run_date       date,

    timezone            text NOT NULL,              -- IANA, from customer profile

    -- Opaque gateway vault reference. Card data never enters this system.
    payment_token_ref   text,

    shipping_address_id text,
    discount_pct        numeric(5,2) NOT NULL DEFAULT 0,
    paused_until        date,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT schedules_interval_days_range
        CHECK (interval_days BETWEEN 7 AND 180),
    CONSTRAINT schedules_discount_pct_range
        CHECK (discount_pct >= 0 AND discount_pct <= 100),
    -- paused_until is null unless the schedule is paused (spec §3).
    CONSTRAINT schedules_paused_until_requires_paused
        CHECK (paused_until IS NULL OR status = 'paused')
);

CREATE INDEX schedules_next_run_date_active_idx
    ON schedules (next_run_date)
    WHERE status = 'active';

CREATE INDEX schedules_customer_id_idx ON schedules (customer_id);

CREATE TABLE schedule_items (
    id          uuid PRIMARY KEY,
    schedule_id uuid NOT NULL REFERENCES schedules (id) ON DELETE CASCADE,
    sku         text NOT NULL,
    quantity    integer NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT schedule_items_quantity_positive CHECK (quantity > 0),
    CONSTRAINT schedule_items_unique_sku UNIQUE (schedule_id, sku)
);

CREATE TABLE occurrences (
    id            uuid PRIMARY KEY,
    schedule_id   uuid NOT NULL REFERENCES schedules (id) ON DELETE CASCADE,
    sequence_no   integer NOT NULL,
    scheduled_for date NOT NULL,
    status        occurrence_status NOT NULL DEFAULT 'planned',
    order_id      text,                             -- WooCommerce order, null until placed

    -- schedule_id:sequence_no. This unique constraint is the whole safety story
    -- for order creation (spec §3): a retry, a duplicate queue delivery, or a
    -- redeployment mid-run must never produce a second charge. The same key is
    -- passed to the gateway. Never drop or weaken it.
    idempotency_key text NOT NULL,

    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT occurrences_idempotency_key_unique UNIQUE (idempotency_key),
    CONSTRAINT occurrences_sequence_unique UNIQUE (schedule_id, sequence_no),
    CONSTRAINT occurrences_sequence_no_positive CHECK (sequence_no > 0),
    -- An order reference exists only once the occurrence has been placed.
    CONSTRAINT occurrences_order_requires_placed
        CHECK (order_id IS NULL OR status = 'placed')
);

CREATE INDEX occurrences_schedule_sequence_idx
    ON occurrences (schedule_id, sequence_no);

CREATE INDEX occurrences_due_idx
    ON occurrences (scheduled_for)
    WHERE status IN ('planned', 'pending');

-- Append-only audit log. Every state transition, with actor, reason code and
-- payload diff. This is the CQRS/event-sourcing seam: the read models in spec §8
-- project off this table, and churn analysis needs the reason codes.
-- There is no UPDATE or DELETE path to this table anywhere in the service.
CREATE TABLE schedule_events (
    id          bigserial PRIMARY KEY,
    schedule_id uuid NOT NULL REFERENCES schedules (id) ON DELETE CASCADE,
    event_type  text NOT NULL,
    actor       schedule_event_actor NOT NULL,
    reason_code text,
    payload     jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX schedule_events_schedule_id_idx
    ON schedule_events (schedule_id, id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS schedule_events;
DROP TABLE IF EXISTS occurrences;
DROP TABLE IF EXISTS schedule_items;
DROP TABLE IF EXISTS schedules;
DROP TYPE IF EXISTS schedule_event_actor;
DROP TYPE IF EXISTS occurrence_status;
DROP TYPE IF EXISTS schedule_status;
-- +goose StatementEnd
