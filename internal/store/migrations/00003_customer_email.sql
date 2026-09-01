-- +goose Up
-- +goose StatementBegin

-- Lifecycle email is this service's job, not WooCommerce's (spec §2 scope:
-- "Lifecycle email via Postmark"), but nothing in the schema has ever stored an
-- address to send to — customer_id is only a WooCommerce customer reference.
-- Phase 4 (notifications) cannot function without one, so this is added now
-- rather than deferred: unlike the gaps documented in ADR 0006, this one blocks
-- the very phase it's needed for, not some later phase.
--
-- NOT NULL with no default: there is no real customer data in this system yet
-- (pre-Phase-2, pre-launch), so this is the right time to require it outright
-- rather than add it nullable "for now" and revisit later.
ALTER TABLE schedules ADD COLUMN customer_email text NOT NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE schedules DROP COLUMN customer_email;
-- +goose StatementEnd
