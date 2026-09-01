-- +goose Up
-- +goose StatementBegin

-- Backs the Phase 4 notification dispatcher: a transactional outbox off
-- schedule_events, the same append-only source of truth the spec §8 read models
-- project from. A row's existence here IS the "sent" fact -- nothing ever updates
-- one, same discipline as schedule_events itself.
--
-- event_id is the primary key rather than a separate id: it is what makes "has
-- this event already been notified" a single indexed lookup, and it is what a
-- UNIQUE-violation-as-ErrDuplicateNotification check is keyed on.
CREATE TABLE notification_log (
    event_id bigint PRIMARY KEY REFERENCES schedule_events (id) ON DELETE CASCADE,
    kind     text NOT NULL,
    sent_at  timestamptz NOT NULL DEFAULT now()
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS notification_log;
-- +goose StatementEnd
