// Package store is the data-access layer.
//
// Callers depend on Repository, not on this implementation, so swapping the driver
// or the query builder is an adapter change rather than a rewrite (spec §12,
// docs/adr/0003).
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/EPW80/replenishment-system/internal/domain"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// ErrDuplicateOccurrence is returned when an occurrence with the same idempotency
// key already exists.
//
// This is not an error condition to route around — it is the safety property working.
// Spec §3: a retry, a duplicate queue delivery, or a redeployment mid-run must never
// produce a second charge. Callers treat it as "already done", never as "try again
// with a new key".
var ErrDuplicateOccurrence = errors.New("occurrence already exists for this idempotency key")

// ErrDuplicateSchedule is returned when a schedule with the same origin_order_id
// already exists.
//
// Same shape as ErrDuplicateOccurrence: a retried POST /schedules is "already done",
// not "try again with a new key". Callers look up and return the existing schedule.
var ErrDuplicateSchedule = errors.New("schedule already exists for this origin order")

// Repository is the persistence interface the rest of the service depends on.
//
// Note there is no UpdateEvent or DeleteEvent. The event log is append-only by
// construction (spec §3): the absence of those methods is the enforcement.
type Repository interface {
	CreateSchedule(ctx context.Context, s domain.Schedule, items []domain.ScheduleItem) error
	GetSchedule(ctx context.Context, id string, scope Scope) (domain.Schedule, error)

	// GetScheduleByOriginOrderID looks up a schedule by the checkout it originated
	// from. It is unscoped: it backs POST /schedules' idempotent-retry path, which
	// runs on the service credential rather than a customer token, and
	// origin_order_id is unique regardless of customer.
	GetScheduleByOriginOrderID(ctx context.Context, originOrderID string) (domain.Schedule, error)

	// GetScheduleForUpdate reads a schedule and holds its row until the surrounding
	// transaction ends. It is the serialization point for everything that changes a
	// schedule: a caller that reads through it, validates, and writes before
	// committing cannot have its decision invalidated in between.
	//
	// It requires a transaction — outside one there is nothing to hold the lock for,
	// and returning an unlocked read would be a silent downgrade of the guarantee
	// its callers are relying on.
	GetScheduleForUpdate(ctx context.Context, id string, scope Scope) (domain.Schedule, error)
	ListSchedulesByCustomer(ctx context.Context, customerID string) ([]domain.Schedule, error)
	ListActiveSchedules(ctx context.Context) ([]domain.Schedule, error)

	// ListSchedulesDueToResume returns paused schedules whose paused_until has come
	// due, for the sweep that ends a timed pause.
	//
	// It selects one day wider than asked, because "due" is a question about the
	// customer's calendar and this query only knows the run date. A schedule in a
	// timezone ahead of the run date is already on its resume day while UTC is not,
	// so the extra day is what keeps it from waiting an entire cycle. The caller
	// narrows the candidates against each schedule's own timezone, the same way
	// Pause validated the date going in.
	//
	// A NULL paused_until is an indefinite pause and is never returned.
	ListSchedulesDueToResume(ctx context.Context, on domain.Date) ([]domain.Schedule, error)
	ListScheduleItems(ctx context.Context, scheduleID string, scope Scope) ([]domain.ScheduleItem, error)
	UpdateScheduleNextRun(ctx context.Context, scheduleID string, next *domain.Date) error

	CreateOccurrence(ctx context.Context, o domain.Occurrence) error
	ListOccurrences(ctx context.Context, scheduleID string, scope Scope) ([]domain.Occurrence, error)
	ListPlannedOccurrences(ctx context.Context, scheduleID string) ([]domain.Occurrence, error)
	CountFutureplannedOccurrences(ctx context.Context, scheduleID string, after domain.Date) (int, error)
	MaxSequenceNo(ctx context.Context, scheduleID string) (int, error)

	AppendEvent(ctx context.Context, e domain.ScheduleEvent) error
	ListEvents(ctx context.Context, scheduleID string) ([]domain.ScheduleEvent, error)

	// EventExistsWithKey reports whether an event of eventType already carries this
	// idempotency key for this schedule. SkipNext and Defer check it before resolving
	// a target occurrence, so a retry never resolves a different one than the first
	// call did (docs/adr/0009).
	EventExistsWithKey(ctx context.Context, scheduleID, eventType, idempotencyKey string) (bool, error)

	// Spec §6 transitions. Each mutates state that the event log must agree with,
	// so callers run them inside InTx together with their AppendEvent.
	UpdateScheduleStatus(ctx context.Context, scheduleID string, status domain.ScheduleStatus, pausedUntil *domain.Date) error
	UpdateScheduleCadence(ctx context.Context, scheduleID string, intervalDays int, anchor domain.Date) error
	UpdateOccurrenceStatus(ctx context.Context, occurrenceID string, status domain.OccurrenceStatus) error
	UpdateOccurrenceDate(ctx context.Context, occurrenceID string, scheduledFor domain.Date) error
	CancelUnexecutedOccurrences(ctx context.Context, scheduleID string) (int, error)
	CancelPlannedOccurrences(ctx context.Context, scheduleID string) (int, error)
	NextActionableOccurrence(ctx context.Context, scheduleID string) (domain.Occurrence, error)
	LastPlacedOccurrence(ctx context.Context, scheduleID string) (domain.Occurrence, error)
	LatestScheduledDate(ctx context.Context, scheduleID string) (*domain.Date, error)

	// InTx runs fn against a repository scoped to a single database transaction.
	// A transition and the event recording it commit together or not at all: an
	// event log that disagrees with the state it describes is not an audit trail.
	InTx(ctx context.Context, fn func(Repository) error) error
}

// dbtx is the subset of *sql.DB that the queries below need. *sql.Tx satisfies it
// too, which is what lets one repository implementation serve both.
type dbtx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// PostgresRepository implements Repository against Postgres.
type PostgresRepository struct {
	// db is nil when this repository is scoped to a transaction, which is how
	// InTx tells the two cases apart.
	db   *sql.DB
	conn dbtx
}

// New returns a Repository backed by db.
func New(db *sql.DB) *PostgresRepository { return &PostgresRepository{db: db, conn: db} }

// InTx runs fn inside one transaction, committing if it returns nil and rolling back
// otherwise.
//
// Calling it on a repository that is already transaction-scoped runs fn in that same
// transaction rather than nesting a new one, so a service method composed of other
// service methods still commits atomically.
func (r *PostgresRepository) InTx(ctx context.Context, fn func(Repository) error) error {
	return r.inTx(ctx, func(p *PostgresRepository) error { return fn(p) })
}

// inTx is InTx over the concrete type, so callers inside this package reach the
// transaction's connection directly instead of asserting their way back to it.
func (r *PostgresRepository) inTx(ctx context.Context, fn func(*PostgresRepository) error) error {
	if r.db == nil {
		return fn(r)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := fn(&PostgresRepository{conn: tx}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// isUniqueViolation reports whether err is a Postgres unique-constraint violation on
// the named constraint. Matched on the message rather than the pgx error type so the
// interface stays driver-agnostic.
func isUniqueViolation(err error, constraint string) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "SQLSTATE 23505") && strings.Contains(msg, constraint)
}

// CreateSchedule inserts one schedule and its items.
//
// A duplicate origin_order_id returns ErrDuplicateSchedule rather than a generic
// error, because the caller's correct response is to look up and return the schedule
// that already exists — never to retry with a different origin_order_id, which would
// defeat the point.
func (r *PostgresRepository) CreateSchedule(ctx context.Context, s domain.Schedule, items []domain.ScheduleItem) error {
	if err := domain.ValidateInterval(s.IntervalDays); err != nil {
		return err
	}
	if _, err := time.LoadLocation(s.Timezone); err != nil {
		return fmt.Errorf("invalid timezone %q: %w", s.Timezone, err)
	}

	return r.inTx(ctx, func(tx *PostgresRepository) error {
		_, err := tx.conn.ExecContext(ctx, `
			INSERT INTO schedules (
				id, customer_id, status, interval_days, anchor_date, next_run_date,
				timezone, payment_token_ref, shipping_address_id, discount_pct, paused_until,
				origin_order_id
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			s.ID, s.CustomerID, string(s.Status), s.IntervalDays, s.AnchorDate.ToTime(),
			datePtr(s.NextRunDate), s.Timezone, nullStr(s.PaymentTokenRef),
			nullStr(s.ShippingAddressID), s.DiscountPct, datePtr(s.PausedUntil), s.OriginOrderID)
		if isUniqueViolation(err, "schedules_origin_order_id_unique") {
			return ErrDuplicateSchedule
		}
		if err != nil {
			return fmt.Errorf("insert schedule: %w", err)
		}

		for _, it := range items {
			if _, err := tx.conn.ExecContext(ctx, `
				INSERT INTO schedule_items (id, schedule_id, sku, quantity)
				VALUES ($1,$2,$3,$4)`,
				it.ID, s.ID, it.SKU, it.Quantity); err != nil {
				return fmt.Errorf("insert schedule item %q: %w", it.SKU, err)
			}
		}
		return nil
	})
}

const scheduleColumns = `id, customer_id, status, interval_days, anchor_date,
	next_run_date, timezone, coalesce(payment_token_ref,''),
	coalesce(shipping_address_id,''), discount_pct, paused_until, origin_order_id,
	created_at, updated_at`

func scanSchedule(row interface{ Scan(...any) error }) (domain.Schedule, error) {
	var (
		s          domain.Schedule
		status     string
		anchor     time.Time
		next, paus sql.NullTime
	)
	err := row.Scan(&s.ID, &s.CustomerID, &status, &s.IntervalDays, &anchor, &next,
		&s.Timezone, &s.PaymentTokenRef, &s.ShippingAddressID, &s.DiscountPct, &paus,
		&s.OriginOrderID, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return domain.Schedule{}, err
	}
	s.Status = domain.ScheduleStatus(status)
	s.AnchorDate = domain.DateOf(anchor.UTC())
	if next.Valid {
		d := domain.DateOf(next.Time.UTC())
		s.NextRunDate = &d
	}
	if paus.Valid {
		d := domain.DateOf(paus.Time.UTC())
		s.PausedUntil = &d
	}
	return s, nil
}

// GetSchedule reads one schedule, limited to what scope allows.
//
// A schedule that exists but belongs to another customer returns ErrNotFound, the same
// as one that does not exist. The handlers map that to 404, which is the point:
// distinguishing "not yours" from "no such thing" would confirm to an attacker which
// schedule IDs are real.
func (r *PostgresRepository) GetSchedule(ctx context.Context, id string, scope Scope) (domain.Schedule, error) {
	where, args := scope.filterOwn(2)
	row := r.conn.QueryRowContext(ctx,
		`SELECT `+scheduleColumns+` FROM schedules WHERE id = $1`+where,
		append([]any{id}, args...)...)
	s, err := scanSchedule(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Schedule{}, ErrNotFound
	}
	if err != nil {
		return domain.Schedule{}, fmt.Errorf("get schedule: %w", err)
	}
	return s, nil
}

// GetScheduleByOriginOrderID reads the schedule for a checkout, unscoped (see the
// Repository interface doc).
func (r *PostgresRepository) GetScheduleByOriginOrderID(ctx context.Context, originOrderID string) (domain.Schedule, error) {
	row := r.conn.QueryRowContext(ctx,
		`SELECT `+scheduleColumns+` FROM schedules WHERE origin_order_id = $1`, originOrderID)
	s, err := scanSchedule(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Schedule{}, ErrNotFound
	}
	if err != nil {
		return domain.Schedule{}, fmt.Errorf("get schedule by origin order id: %w", err)
	}
	return s, nil
}

// ErrNoTransaction is returned when a call that must serialize against other writers
// is made outside a transaction.
var ErrNoTransaction = errors.New("this call requires a transaction")

// GetScheduleForUpdate reads a schedule and locks its row for the rest of the
// transaction (spec §6).
//
// This is what makes a transition atomic against another transition. Reading, checking
// the precondition, and writing are three steps, and without the lock another caller
// can commit a status change in the gaps: both callers validate against a state that
// was true when they looked and false by the time they wrote, and both apply. The row
// lock collapses that window — the second caller waits, then re-reads the committed
// row and finds its precondition no longer holds, which surfaces as a 409 rather than
// as a second conflicting write.
//
// It refuses to run outside a transaction. A FOR UPDATE in its own implicit
// transaction releases the lock the moment the statement returns, so it would read as
// locking while guaranteeing nothing — the kind of false assurance that is worse than
// no lock at all, because callers stop thinking about the race.
func (r *PostgresRepository) GetScheduleForUpdate(ctx context.Context, id string, scope Scope) (domain.Schedule, error) {
	if r.db != nil {
		return domain.Schedule{}, ErrNoTransaction
	}

	where, args := scope.filterOwn(2)
	row := r.conn.QueryRowContext(ctx,
		`SELECT `+scheduleColumns+` FROM schedules WHERE id = $1`+where+` FOR UPDATE`,
		append([]any{id}, args...)...)
	s, err := scanSchedule(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Schedule{}, ErrNotFound
	}
	if err != nil {
		return domain.Schedule{}, fmt.Errorf("get schedule for update: %w", err)
	}
	return s, nil
}

func (r *PostgresRepository) querySchedules(ctx context.Context, where string, args ...any) ([]domain.Schedule, error) {
	rows, err := r.conn.QueryContext(ctx, `SELECT `+scheduleColumns+` FROM schedules `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("query schedules: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Schedule
	for rows.Next() {
		s, err := scanSchedule(rows)
		if err != nil {
			return nil, fmt.Errorf("scan schedule: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) ListSchedulesByCustomer(ctx context.Context, customerID string) ([]domain.Schedule, error) {
	return r.querySchedules(ctx, `WHERE customer_id = $1 ORDER BY created_at`, customerID)
}

func (r *PostgresRepository) ListActiveSchedules(ctx context.Context) ([]domain.Schedule, error) {
	return r.querySchedules(ctx, `WHERE status = 'active' ORDER BY next_run_date NULLS FIRST, created_at`)
}

func (r *PostgresRepository) ListSchedulesDueToResume(ctx context.Context, on domain.Date) ([]domain.Schedule, error) {
	// paused_until IS NOT NULL is not redundant with the <= comparison -- it says in
	// the query what the contract promises, so an indefinite pause cannot start
	// resuming itself if this predicate is ever edited.
	return r.querySchedules(ctx,
		`WHERE status = 'paused'
		   AND paused_until IS NOT NULL
		   AND paused_until <= $1
		 ORDER BY paused_until, created_at`,
		on.AddDays(1).ToTime())
}

func (r *PostgresRepository) ListScheduleItems(ctx context.Context, scheduleID string, scope Scope) ([]domain.ScheduleItem, error) {
	where, args := scope.filterVia("schedule_id", 2)
	rows, err := r.conn.QueryContext(ctx, `
		SELECT id, schedule_id, sku, quantity, created_at
		FROM schedule_items WHERE schedule_id = $1`+where+` ORDER BY sku`,
		append([]any{scheduleID}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("query schedule items: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.ScheduleItem
	for rows.Next() {
		var it domain.ScheduleItem
		if err := rows.Scan(&it.ID, &it.ScheduleID, &it.SKU, &it.Quantity, &it.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan schedule item: %w", err)
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) UpdateScheduleNextRun(ctx context.Context, scheduleID string, next *domain.Date) error {
	res, err := r.conn.ExecContext(ctx,
		`UPDATE schedules SET next_run_date = $2, updated_at = now() WHERE id = $1`,
		scheduleID, datePtr(next))
	if err != nil {
		return fmt.Errorf("update next_run_date: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// CreateOccurrence inserts one occurrence.
//
// A duplicate idempotency key returns ErrDuplicateOccurrence rather than a generic
// error, because the caller's correct response is to treat the occurrence as already
// created — never to retry with a different key.
func (r *PostgresRepository) CreateOccurrence(ctx context.Context, o domain.Occurrence) error {
	_, err := r.conn.ExecContext(ctx, `
		INSERT INTO occurrences (
			id, schedule_id, sequence_no, scheduled_for, status, order_id, idempotency_key
		) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		o.ID, o.ScheduleID, o.SequenceNo, o.ScheduledFor.ToTime(), string(o.Status),
		o.OrderID, o.IdempotencyKey)
	switch {
	case isUniqueViolation(err, "occurrences_idempotency_key_unique"),
		isUniqueViolation(err, "occurrences_sequence_unique"):
		return ErrDuplicateOccurrence
	case err != nil:
		return fmt.Errorf("insert occurrence: %w", err)
	}
	return nil
}

const occurrenceColumns = `id, schedule_id, sequence_no, scheduled_for, status,
	order_id, idempotency_key, created_at, updated_at`

func (r *PostgresRepository) queryOccurrences(ctx context.Context, where string, args ...any) ([]domain.Occurrence, error) {
	rows, err := r.conn.QueryContext(ctx, `SELECT `+occurrenceColumns+` FROM occurrences `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("query occurrences: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Occurrence
	for rows.Next() {
		var (
			o         domain.Occurrence
			status    string
			scheduled time.Time
			orderID   sql.NullString
		)
		if err := rows.Scan(&o.ID, &o.ScheduleID, &o.SequenceNo, &scheduled, &status,
			&orderID, &o.IdempotencyKey, &o.CreatedAt, &o.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan occurrence: %w", err)
		}
		o.Status = domain.OccurrenceStatus(status)
		o.ScheduledFor = domain.DateOf(scheduled.UTC())
		if orderID.Valid {
			v := orderID.String
			o.OrderID = &v
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) ListOccurrences(ctx context.Context, scheduleID string, scope Scope) ([]domain.Occurrence, error) {
	where, args := scope.filterVia("schedule_id", 2)
	return r.queryOccurrences(ctx,
		`WHERE schedule_id = $1`+where+` ORDER BY sequence_no`,
		append([]any{scheduleID}, args...)...)
}

func (r *PostgresRepository) ListPlannedOccurrences(ctx context.Context, scheduleID string) ([]domain.Occurrence, error) {
	return r.queryOccurrences(ctx,
		`WHERE schedule_id = $1 AND status = 'planned' ORDER BY sequence_no`, scheduleID)
}

// CountFutureplannedOccurrences counts planned occurrences scheduled after a date.
// The materializer uses it to decide how many more to create.
func (r *PostgresRepository) CountFutureplannedOccurrences(ctx context.Context, scheduleID string, after domain.Date) (int, error) {
	var n int
	err := r.conn.QueryRowContext(ctx, `
		SELECT count(*) FROM occurrences
		WHERE schedule_id = $1 AND status = 'planned' AND scheduled_for > $2`,
		scheduleID, after.ToTime()).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count planned occurrences: %w", err)
	}
	return n, nil
}

// MaxSequenceNo returns the highest sequence number used by a schedule, or 0.
func (r *PostgresRepository) MaxSequenceNo(ctx context.Context, scheduleID string) (int, error) {
	var n sql.NullInt64
	err := r.conn.QueryRowContext(ctx,
		`SELECT max(sequence_no) FROM occurrences WHERE schedule_id = $1`, scheduleID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("max sequence_no: %w", err)
	}
	if !n.Valid {
		return 0, nil
	}
	return int(n.Int64), nil
}

// AppendEvent writes one entry to the append-only audit log.
//
// There is deliberately no counterpart that updates or deletes an event: the read
// models in spec §8 project off this table, and an editable log is not an audit
// trail.
func (r *PostgresRepository) AppendEvent(ctx context.Context, e domain.ScheduleEvent) error {
	payload := e.Payload
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	_, err := r.conn.ExecContext(ctx, `
		INSERT INTO schedule_events (schedule_id, event_type, actor, reason_code, payload, idempotency_key)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		e.ScheduleID, e.EventType, string(e.Actor), e.ReasonCode, payload, e.IdempotencyKey)
	if err != nil {
		return fmt.Errorf("append event: %w", err)
	}
	return nil
}

// EventExistsWithKey reports whether (schedule_id, event_type, idempotency_key) has
// already been recorded.
//
// The row lock SkipNext and Defer hold on the schedule is what makes this check safe
// to act on: nothing else can be appending a competing event for this schedule between
// this read and the mutation that follows it.
func (r *PostgresRepository) EventExistsWithKey(ctx context.Context, scheduleID, eventType, idempotencyKey string) (bool, error) {
	var exists bool
	err := r.conn.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM schedule_events
			WHERE schedule_id = $1 AND event_type = $2 AND idempotency_key = $3
		)`, scheduleID, eventType, idempotencyKey).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check idempotency key: %w", err)
	}
	return exists, nil
}

func (r *PostgresRepository) ListEvents(ctx context.Context, scheduleID string) ([]domain.ScheduleEvent, error) {
	rows, err := r.conn.QueryContext(ctx, `
		SELECT id, schedule_id, event_type, actor, reason_code, payload, idempotency_key, created_at
		FROM schedule_events WHERE schedule_id = $1 ORDER BY id`, scheduleID)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.ScheduleEvent
	for rows.Next() {
		var (
			e      domain.ScheduleEvent
			actor  string
			reason sql.NullString
			key    sql.NullString
		)
		if err := rows.Scan(&e.ID, &e.ScheduleID, &e.EventType, &actor, &reason,
			&e.Payload, &key, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		e.Actor = domain.EventActor(actor)
		if reason.Valid {
			v := reason.String
			e.ReasonCode = &v
		}
		if key.Valid {
			v := key.String
			e.IdempotencyKey = &v
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func datePtr(d *domain.Date) any {
	if d == nil {
		return nil
	}
	return d.ToTime()
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// UpdateScheduleStatus sets a schedule's status and paused_until together.
//
// They move in one statement because the schema ties them: the
// schedules_paused_until_requires_paused CHECK rejects a paused_until on a schedule
// that is not paused, so resuming or canceling must clear the date in the same UPDATE
// that changes the status.
func (r *PostgresRepository) UpdateScheduleStatus(ctx context.Context, scheduleID string, status domain.ScheduleStatus, pausedUntil *domain.Date) error {
	if status != domain.SchedulePaused && pausedUntil != nil {
		return fmt.Errorf("paused_until is only valid on a paused schedule, got status %s", status)
	}
	res, err := r.conn.ExecContext(ctx, `
		UPDATE schedules SET status = $2, paused_until = $3, updated_at = now()
		WHERE id = $1`,
		scheduleID, string(status), datePtr(pausedUntil))
	if err != nil {
		return fmt.Errorf("update schedule status: %w", err)
	}
	return checkAffected(res)
}

// UpdateScheduleCadence sets the interval and re-anchors the schedule.
//
// Re-anchoring is what keeps spec §3's rule intact across a resume or a cadence
// change: every future date is recomputed as anchor + (n × interval_days) from the new
// anchor, so nothing accumulates from the previous cadence.
func (r *PostgresRepository) UpdateScheduleCadence(ctx context.Context, scheduleID string, intervalDays int, anchor domain.Date) error {
	if err := domain.ValidateInterval(intervalDays); err != nil {
		return err
	}
	res, err := r.conn.ExecContext(ctx, `
		UPDATE schedules SET interval_days = $2, anchor_date = $3, updated_at = now()
		WHERE id = $1`,
		scheduleID, intervalDays, anchor.ToTime())
	if err != nil {
		return fmt.Errorf("update schedule cadence: %w", err)
	}
	return checkAffected(res)
}

// UpdateOccurrenceStatus moves one occurrence to a new status.
//
// It refuses to touch an occurrence that already carries an order reference. Spec §3
// says a placed order must never be rewritten, and the occurrences_order_requires_placed
// CHECK would reject the write anyway — failing here gives the caller a usable error
// instead of a constraint violation.
func (r *PostgresRepository) UpdateOccurrenceStatus(ctx context.Context, occurrenceID string, status domain.OccurrenceStatus) error {
	res, err := r.conn.ExecContext(ctx, `
		UPDATE occurrences SET status = $2, updated_at = now()
		WHERE id = $1 AND order_id IS NULL`,
		occurrenceID, string(status))
	if err != nil {
		return fmt.Errorf("update occurrence status: %w", err)
	}
	return checkAffected(res)
}

// UpdateOccurrenceDate moves one occurrence to a new date — the defer action (spec §6).
//
// Only the occurrence moves. The schedule's anchor is untouched, which is the whole
// point of defer: a customer who pushes one shipment out returns to their normal
// rhythm afterward rather than permanently sliding.
func (r *PostgresRepository) UpdateOccurrenceDate(ctx context.Context, occurrenceID string, scheduledFor domain.Date) error {
	res, err := r.conn.ExecContext(ctx, `
		UPDATE occurrences SET scheduled_for = $2, updated_at = now()
		WHERE id = $1 AND status IN ('planned','pending')`,
		occurrenceID, scheduledFor.ToTime())
	if err != nil {
		return fmt.Errorf("update occurrence date: %w", err)
	}
	return checkAffected(res)
}

// CancelUnexecutedOccurrences cancels every occurrence not yet acted on — used by
// pause and cancel (spec §6). Placed, skipped, failed and already-canceled
// occurrences are settled and left alone.
func (r *PostgresRepository) CancelUnexecutedOccurrences(ctx context.Context, scheduleID string) (int, error) {
	return r.cancelOccurrences(ctx, scheduleID, []string{"planned", "pending"})
}

// CancelPlannedOccurrences cancels only the planned ones, leaving pending untouched.
//
// This is the narrower sweep a cadence change needs: spec §5 says a cadence change
// "rewrites unexecuted planned occurrences and leaves pending ones alone unless the
// customer explicitly skips." A pending occurrence has already had its pre-billing
// notice sent (§5 step 2), so moving it out from under the customer would contradict
// the notice they were just given.
func (r *PostgresRepository) CancelPlannedOccurrences(ctx context.Context, scheduleID string) (int, error) {
	return r.cancelOccurrences(ctx, scheduleID, []string{"planned"})
}

func (r *PostgresRepository) cancelOccurrences(ctx context.Context, scheduleID string, statuses []string) (int, error) {
	res, err := r.conn.ExecContext(ctx, `
		UPDATE occurrences SET status = 'canceled', updated_at = now()
		WHERE schedule_id = $1 AND status = ANY($2) AND order_id IS NULL`,
		scheduleID, pqTextArray(statuses))
	if err != nil {
		return 0, fmt.Errorf("cancel occurrences: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, nil //nolint:nilerr // the update succeeded; only the count is unavailable
	}
	return int(n), nil
}

// NextActionableOccurrence returns the soonest occurrence a customer can still skip or
// defer — the "next occurrence" in the spec §6 preconditions.
//
// Ordered by date rather than by sequence number: after a defer, the two orders can
// disagree, and what the customer means by "next" is the one arriving soonest.
func (r *PostgresRepository) NextActionableOccurrence(ctx context.Context, scheduleID string) (domain.Occurrence, error) {
	return r.queryOneOccurrence(ctx, `
		WHERE schedule_id = $1 AND status IN ('planned','pending')
		ORDER BY scheduled_for, sequence_no LIMIT 1`, scheduleID)
}

// LastPlacedOccurrence returns the most recently placed occurrence, or ErrNotFound if
// the schedule has never placed an order. change_cadence re-anchors to it (spec §6).
func (r *PostgresRepository) LastPlacedOccurrence(ctx context.Context, scheduleID string) (domain.Occurrence, error) {
	return r.queryOneOccurrence(ctx, `
		WHERE schedule_id = $1 AND status = 'placed'
		ORDER BY scheduled_for DESC, sequence_no DESC LIMIT 1`, scheduleID)
}

func (r *PostgresRepository) queryOneOccurrence(ctx context.Context, where string, args ...any) (domain.Occurrence, error) {
	out, err := r.queryOccurrences(ctx, where, args...)
	if err != nil {
		return domain.Occurrence{}, err
	}
	if len(out) == 0 {
		return domain.Occurrence{}, ErrNotFound
	}
	return out[0], nil
}

// LatestScheduledDate returns the furthest-out date a schedule already has an
// unsettled occurrence on, or nil if it has none.
//
// The materializer uses it as the point to continue the cadence from, which is what
// keeps it from planning a date that is already spoken for after a defer or a
// re-anchor.
func (r *PostgresRepository) LatestScheduledDate(ctx context.Context, scheduleID string) (*domain.Date, error) {
	var t sql.NullTime
	err := r.conn.QueryRowContext(ctx, `
		SELECT max(scheduled_for) FROM occurrences
		WHERE schedule_id = $1 AND status IN ('planned','pending')`, scheduleID).Scan(&t)
	if err != nil {
		return nil, fmt.Errorf("latest scheduled date: %w", err)
	}
	if !t.Valid {
		return nil, nil
	}
	d := domain.DateOf(t.Time.UTC())
	return &d, nil
}

// checkAffected turns "updated nothing" into ErrNotFound, so a caller acting on a row
// that has since changed underneath it gets a clear answer rather than a silent no-op.
func checkAffected(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return nil //nolint:nilerr // the update succeeded; only the count is unavailable
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// pqTextArray renders a Go slice as a Postgres text[] literal for = ANY($n).
//
// The values are always in-code status constants, never user input; even so the
// elements are quoted and escaped rather than concatenated raw, so this cannot become
// an injection point if a caller is added later.
func pqTextArray(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, v := range values {
		quoted = append(quoted, `"`+strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v)+`"`)
	}
	return "{" + strings.Join(quoted, ",") + "}"
}
