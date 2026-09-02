-- +goose Up
-- +goose StatementBegin

-- The sweep that ends a timed pause (spec §6 resume) scans for schedules whose
-- paused_until has come due. Partial rather than full: only paused rows can carry a
-- paused_until at all -- schedules_paused_until_requires_paused in 00001 enforces
-- that -- so indexing the other statuses would store nothing but NULLs.
--
-- Adding this index changes no data and no existing query plan that matters, so the
-- previously deployed binary keeps working against it unchanged.

CREATE INDEX schedules_paused_until_due_idx
    ON schedules (paused_until)
    WHERE status = 'paused';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS schedules_paused_until_due_idx;
-- +goose StatementEnd
