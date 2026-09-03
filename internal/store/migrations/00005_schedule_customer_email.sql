-- +goose Up
-- +goose StatementBegin

-- Phase 4 (spec §7, issue #4) needs somewhere to send a transactional email when a
-- schedule is created, paused, resumed, or canceled. No email address exists anywhere
-- in this schema today -- customer_id is a WooCommerce customer ID, not an address.
--
-- NOT NULL DEFAULT '' rather than nullable: a schedule with no address on file is a
-- real, unremarkable state (a pre-Phase-4 row, or a caller that never sends one), and
-- the notification dispatcher (a later PR) simply has nothing to send for it -- an
-- empty string reads as "no address" without needing a separate NULL check anywhere
-- that scans this column. Validation (a well-formed address) happens in Go
-- (domain.ValidateEmail), not here -- this repo validates shape in application code,
-- not via SQL CHECK, the same way ValidateInterval and ValidateCancellationReason do.
ALTER TABLE schedules
    ADD COLUMN customer_email text NOT NULL DEFAULT '';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE schedules DROP COLUMN IF EXISTS customer_email;
-- +goose StatementEnd
