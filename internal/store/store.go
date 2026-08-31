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

// Repository is the persistence interface the rest of the service depends on.
//
// Note there is no UpdateEvent or DeleteEvent. The event log is append-only by
// construction (spec §3): the absence of those methods is the enforcement.
type Repository interface {
	CreateSchedule(ctx context.Context, s domain.Schedule, items []domain.ScheduleItem) error
	GetSchedule(ctx context.Context, id string) (domain.Schedule, error)
	ListSchedulesByCustomer(ctx context.Context, customerID string) ([]domain.Schedule, error)
	ListActiveSchedules(ctx context.Context) ([]domain.Schedule, error)
	ListScheduleItems(ctx context.Context, scheduleID string) ([]domain.ScheduleItem, error)
	UpdateScheduleNextRun(ctx context.Context, scheduleID string, next *domain.Date) error

	CreateOccurrence(ctx context.Context, o domain.Occurrence) error
	ListOccurrences(ctx context.Context, scheduleID string) ([]domain.Occurrence, error)
	ListPlannedOccurrences(ctx context.Context, scheduleID string) ([]domain.Occurrence, error)
	CountFutureplannedOccurrences(ctx context.Context, scheduleID string, after domain.Date) (int, error)
	MaxSequenceNo(ctx context.Context, scheduleID string) (int, error)

	AppendEvent(ctx context.Context, e domain.ScheduleEvent) error
	ListEvents(ctx context.Context, scheduleID string) ([]domain.ScheduleEvent, error)
}

// PostgresRepository implements Repository against Postgres.
type PostgresRepository struct{ db *sql.DB }

// New returns a Repository backed by db.
func New(db *sql.DB) *PostgresRepository { return &PostgresRepository{db: db} }

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

func (r *PostgresRepository) CreateSchedule(ctx context.Context, s domain.Schedule, items []domain.ScheduleItem) error {
	if err := domain.ValidateInterval(s.IntervalDays); err != nil {
		return err
	}
	if _, err := time.LoadLocation(s.Timezone); err != nil {
		return fmt.Errorf("invalid timezone %q: %w", s.Timezone, err)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `
		INSERT INTO schedules (
			id, customer_id, status, interval_days, anchor_date, next_run_date,
			timezone, payment_token_ref, shipping_address_id, discount_pct, paused_until
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		s.ID, s.CustomerID, string(s.Status), s.IntervalDays, s.AnchorDate.ToTime(),
		datePtr(s.NextRunDate), s.Timezone, nullStr(s.PaymentTokenRef),
		nullStr(s.ShippingAddressID), s.DiscountPct, datePtr(s.PausedUntil))
	if err != nil {
		return fmt.Errorf("insert schedule: %w", err)
	}

	for _, it := range items {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO schedule_items (id, schedule_id, sku, quantity)
			VALUES ($1,$2,$3,$4)`,
			it.ID, s.ID, it.SKU, it.Quantity); err != nil {
			return fmt.Errorf("insert schedule item %q: %w", it.SKU, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

const scheduleColumns = `id, customer_id, status, interval_days, anchor_date,
	next_run_date, timezone, coalesce(payment_token_ref,''),
	coalesce(shipping_address_id,''), discount_pct, paused_until, created_at, updated_at`

func scanSchedule(row interface{ Scan(...any) error }) (domain.Schedule, error) {
	var (
		s          domain.Schedule
		status     string
		anchor     time.Time
		next, paus sql.NullTime
	)
	err := row.Scan(&s.ID, &s.CustomerID, &status, &s.IntervalDays, &anchor, &next,
		&s.Timezone, &s.PaymentTokenRef, &s.ShippingAddressID, &s.DiscountPct, &paus,
		&s.CreatedAt, &s.UpdatedAt)
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

func (r *PostgresRepository) GetSchedule(ctx context.Context, id string) (domain.Schedule, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE id = $1`, id)
	s, err := scanSchedule(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Schedule{}, ErrNotFound
	}
	if err != nil {
		return domain.Schedule{}, fmt.Errorf("get schedule: %w", err)
	}
	return s, nil
}

func (r *PostgresRepository) querySchedules(ctx context.Context, where string, args ...any) ([]domain.Schedule, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+scheduleColumns+` FROM schedules `+where, args...)
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

func (r *PostgresRepository) ListScheduleItems(ctx context.Context, scheduleID string) ([]domain.ScheduleItem, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, schedule_id, sku, quantity, created_at
		FROM schedule_items WHERE schedule_id = $1 ORDER BY sku`, scheduleID)
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
	res, err := r.db.ExecContext(ctx,
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
	_, err := r.db.ExecContext(ctx, `
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
	rows, err := r.db.QueryContext(ctx, `SELECT `+occurrenceColumns+` FROM occurrences `+where, args...)
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

func (r *PostgresRepository) ListOccurrences(ctx context.Context, scheduleID string) ([]domain.Occurrence, error) {
	return r.queryOccurrences(ctx, `WHERE schedule_id = $1 ORDER BY sequence_no`, scheduleID)
}

func (r *PostgresRepository) ListPlannedOccurrences(ctx context.Context, scheduleID string) ([]domain.Occurrence, error) {
	return r.queryOccurrences(ctx,
		`WHERE schedule_id = $1 AND status = 'planned' ORDER BY sequence_no`, scheduleID)
}

// CountFutureplannedOccurrences counts planned occurrences scheduled after a date.
// The materializer uses it to decide how many more to create.
func (r *PostgresRepository) CountFutureplannedOccurrences(ctx context.Context, scheduleID string, after domain.Date) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
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
	err := r.db.QueryRowContext(ctx,
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
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO schedule_events (schedule_id, event_type, actor, reason_code, payload)
		VALUES ($1,$2,$3,$4,$5)`,
		e.ScheduleID, e.EventType, string(e.Actor), e.ReasonCode, payload)
	if err != nil {
		return fmt.Errorf("append event: %w", err)
	}
	return nil
}

func (r *PostgresRepository) ListEvents(ctx context.Context, scheduleID string) ([]domain.ScheduleEvent, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, schedule_id, event_type, actor, reason_code, payload, created_at
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
		)
		if err := rows.Scan(&e.ID, &e.ScheduleID, &e.EventType, &actor, &reason,
			&e.Payload, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		e.Actor = domain.EventActor(actor)
		if reason.Valid {
			v := reason.String
			e.ReasonCode = &v
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
