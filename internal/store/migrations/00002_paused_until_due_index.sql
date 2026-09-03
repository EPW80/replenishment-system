-- +goose Up
-- +goose StatementBegin

-- The sweep that ends a timed pause (spec §6 resume) scans for schedules whose
-- paused_until has come due. Partial rather than full: only paused rows can carry a
-- paused_until at all -- schedules_paused_until_requires_paused in 00001 enforces
-- that -- so indexing the other statuses would store nothing but NULLs.
--
-- The predicate also excludes NULL paused_until, matching
-- ListSchedulesDueToResume's own "paused_until IS NOT NULL" exactly: an indefinite
-- pause is never a sweep candidate, so it earns no place in an index built for the
-- sweep. Narrower than "status = 'paused'" alone, and the query planner does not have
-- to reconcile a predicate the index doesn't share.
--
-- Adding this index changes no data and no existing query plan that matters, so the
-- previously deployed binary keeps working against it unchanged.

CREATE INDEX schedules_paused_until_due_idx
    ON schedules (paused_until)
    WHERE status = 'paused' AND paused_until IS NOT NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS schedules_paused_until_due_idx;
-- +goose StatementEnd
