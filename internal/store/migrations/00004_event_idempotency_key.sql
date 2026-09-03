-- +goose Up
-- +goose StatementBegin

-- SkipNext and Defer resolve their target occurrence implicitly ("whichever is next
-- actionable"), and that resolution is not stable across a retry: the first call's
-- mutation changes which occurrence is next, so a retried request can skip or defer a
-- different occurrence than the first call did. occurrences.idempotency_key and
-- origin_order_id close this exact failure mode for occurrence and schedule creation
-- -- "same key twice produces one result, always" -- but nothing plays that role for
-- these two actions.
--
-- idempotency_key is nullable: system-authored events (materialize, sweep) and the
-- other four transitions have no retry-ambiguity to guard against (a retried pause,
-- resume, cancel or cadence change already fails its precondition safely, since it
-- acts on the schedule's own status rather than resolving a target occurrence), so
-- they carry no key. Multiple NULLs are unconstrained under a UNIQUE index by
-- Postgres's own semantics, so this needs no partial predicate to allow them.
--
-- The index is scoped to (schedule_id, event_type, idempotency_key) rather than to
-- idempotency_key alone: two different schedules' customers could hand their own
-- clients the same key value, and even on one schedule a skip and a defer are
-- different actions that should not collide on a coincidentally-shared key.
ALTER TABLE schedule_events
    ADD COLUMN idempotency_key text;

CREATE UNIQUE INDEX schedule_events_idempotency_key_unique
    ON schedule_events (schedule_id, event_type, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS schedule_events_idempotency_key_unique;
ALTER TABLE schedule_events DROP COLUMN IF EXISTS idempotency_key;
-- +goose StatementEnd
