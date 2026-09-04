-- +goose Up
-- +goose StatementBegin

-- ClaimNotifiableEvents (internal/store/store.go, issue #4) filters schedule_events by
-- event_type on every run, with no schedule_id predicate. schedule_events' only
-- existing index is (schedule_id, id) (00001), which does nothing for that query --
-- without this, the claim degrades into a full scan of the whole append-only event
-- log as it grows. Added as its own migration rather than folded into 00006, which
-- already merged and is tracked by version number, not content -- editing it in place
-- would silently do nothing for any environment that already applied it.
CREATE INDEX schedule_events_event_type_id_idx
    ON schedule_events (event_type, id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS schedule_events_event_type_id_idx;
-- +goose StatementEnd
