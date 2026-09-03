-- +goose Up
-- +goose StatementBegin

-- POST /schedules had no idempotency protection: a retried checkout request (a
-- timeout, a duplicate webhook delivery, a redeploy mid-request) created a second,
-- fully independent, active schedule for the same checkout. occurrences.idempotency_key
-- only guards against a duplicate occurrence *within* one schedule -- it does nothing
-- when the schedule itself is duplicated.
--
-- origin_order_id is the WooCommerce order ID from the checkout that established the
-- subscription -- a real identifier the caller already has, not an invented key. It is
-- the schedule-creation equivalent of occurrences.idempotency_key: the same value
-- submitted twice must produce one schedule, always.
--
-- The DEFAULT backfills any schedule created before this migration with a unique
-- synthetic value (rather than leaving the column nullable), which is also what keeps
-- this migration backward compatible: application code always supplies a real
-- origin_order_id, but a redeployed pre-migration binary -- which does not know this
-- column exists -- still inserts successfully during a rollback, just without the
-- idempotency guarantee it never had anyway.
ALTER TABLE schedules
    ADD COLUMN origin_order_id text NOT NULL DEFAULT gen_random_uuid()::text;

ALTER TABLE schedules
    ADD CONSTRAINT schedules_origin_order_id_unique UNIQUE (origin_order_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE schedules DROP CONSTRAINT IF EXISTS schedules_origin_order_id_unique;
ALTER TABLE schedules DROP COLUMN IF EXISTS origin_order_id;
-- +goose StatementEnd
