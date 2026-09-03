-- +goose Up
-- +goose StatementBegin

-- The notification dispatcher (internal/notify, issue #4) needs somewhere to record
-- which schedule_events have already had an email sent for them, so a nightly
-- cmd/notify run picks up only genuinely new work and a retried or overlapping run
-- does not resend everything from scratch.
--
-- This deliberately is not a queue populated at write time by the four event-emitting
-- call sites (schedules.go's Create, service.go's Pause/Resume/Cancel). Adding a
-- second write path there would let the outbox drift from the event log it is
-- supposed to describe. Instead the dispatcher derives its work directly from
-- schedule_events with a LEFT JOIN against this table -- a row here means "this event
-- has been attempted," nothing more, and a new notifiable event type is added by
-- extending the dispatcher's query, not by touching any emit site.
--
-- Delivery is deliberately at-least-once, not exactly-once (docs/adr/0010): unlike
-- occurrences.idempotency_key or the skip/defer key (docs/adr/0009), where a duplicate
-- corrupts state, a duplicate confirmation email is cosmetic. So there is no unique
-- constraint here playing that role -- schedule_event_id is unique because each event
-- gets at most one outbox row, not because a duplicate would be unsafe.
--
-- last_attempt_at backs a visibility-timeout style reclaim: a row stuck at 'pending'
-- (cmd/notify claimed it, then crashed before sending or marking the result) is
-- reclaimable once it is older than the dispatcher's own timeout, rather than lost
-- forever. cmd/notify is a short, infrequent one-shot process, not a long-running
-- worker, so this is simpler than a live heartbeat and covers the failure mode that
-- actually matters here.
CREATE TABLE notification_log (
    id                bigserial PRIMARY KEY,
    schedule_event_id bigint NOT NULL REFERENCES schedule_events (id) ON DELETE CASCADE,
    status            text NOT NULL DEFAULT 'pending',
    attempts          integer NOT NULL DEFAULT 0,
    last_attempt_at   timestamptz NOT NULL DEFAULT now(),
    sent_at           timestamptz,
    last_error        text,
    created_at        timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT notification_log_schedule_event_id_unique UNIQUE (schedule_event_id),
    CONSTRAINT notification_log_status_check
        CHECK (status IN ('pending', 'sent', 'failed'))
);

-- ClaimNotifiableEvents filters schedule_events by event_type on every run.
-- schedule_events' only existing index is (schedule_id, id) (00001), which does
-- nothing for a query with no schedule_id predicate -- without this, the claim
-- degrades into a full scan of the whole append-only event log as it grows.
CREATE INDEX schedule_events_event_type_id_idx
    ON schedule_events (event_type, id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS schedule_events_event_type_id_idx;
DROP TABLE IF EXISTS notification_log;
-- +goose StatementEnd
