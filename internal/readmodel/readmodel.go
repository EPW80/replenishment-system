// Package readmodel exposes the spec §8 analytics surface: cadence distribution,
// churn reasons, an occurrence/unit forecast, audience segments, and cohort
// retention.
//
// Every method here reads from a SQL view defined in
// internal/store/migrations/00002_read_models.sql — spec §8 is explicit that these
// are "read-model views, not [queries against] the write tables directly." The
// views hold the aggregation; this package stays thin scan functions over them, the
// same shape internal/store/store.go uses for the write side.
//
// Two of spec §8's five outputs are scoped down from how the spec words them —
// occurrence/unit counts instead of dollar revenue, and cohorts without an
// acquisition-source dimension. See docs/adr/0006-read-models-scoped-to-available-data.md
// for why: this schema has no price and no acquisition-source column, both of
// which are Phase 2 concerns, and inventing either here would mean building that
// part of Phase 2 twice.
package readmodel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/EPW80/replenishment-system/internal/domain"
)

// Segment is one of the closed set of audience segments spec §8 names:
// "paused, failed, canceled-within-90d are three of the highest-intent lists the
// portfolio will ever have."
type Segment string

const (
	SegmentPaused            Segment = "paused"
	SegmentFailed            Segment = "failed"
	SegmentCanceledWithin90d Segment = "canceled_within_90d"
)

// ValidSegments is the closed set AudienceSegment accepts.
var ValidSegments = []Segment{SegmentPaused, SegmentFailed, SegmentCanceledWithin90d}

// ErrInvalidSegment is returned for a segment outside ValidSegments.
//
// A typo'd segment name must fail loudly rather than return an empty result set —
// SegmentFailed legitimately returns zero rows today (nothing produces a 'failed'
// schedule until Phase 4's dunning ladder exists), and that emptiness must stay
// distinguishable from "you asked for a segment that doesn't exist."
var ErrInvalidSegment = errors.New("invalid segment")

func validateSegment(s Segment) error {
	for _, v := range ValidSegments {
		if v == s {
			return nil
		}
	}
	return fmt.Errorf("%w: %q (valid: %v)", ErrInvalidSegment, s, ValidSegments)
}

// CadenceDistributionRow is one (sku, interval_days, status) group from
// v_cadence_distribution. status is included so a caller can compare the cadence
// mix of active schedules against churned ones when answering spec §11 decision #4
// — a schedule canceled for "too_frequent" is itself a signal about that interval.
type CadenceDistributionRow struct {
	SKU           string
	IntervalDays  int
	Status        domain.ScheduleStatus
	ScheduleCount int
}

// ChurnReasonRow is one reason code from v_churn_reasons. Every code in
// domain.CancellationReasons appears exactly once, even at a count of zero —
// see the view's definition for why that matters.
type ChurnReasonRow struct {
	ReasonCode        string
	CancellationCount int
	FirstAt           *time.Time
	LastAt            *time.Time
}

// ForecastRow is one (week, sku) bucket from v_occurrence_forecast. It reports
// counts, not dollars — see the package comment.
type ForecastRow struct {
	WeekStart       domain.Date
	SKU             string
	OccurrenceCount int
	UnitCount       int
}

// SegmentRow is one schedule's membership in an audience segment.
// SegmentSince is nil when the source event doesn't exist yet — see the view's
// 'failed' branch.
type SegmentRow struct {
	CustomerID   string
	ScheduleID   string
	Segment      string
	SegmentSince *time.Time
}

// CohortRow is one (signup month, interval_days, status) group from
// v_cohort_retention.
type CohortRow struct {
	CohortMonth   domain.Date
	IntervalDays  int
	Status        domain.ScheduleStatus
	ScheduleCount int
}

// Repository is the read-model query interface the rest of the service depends
// on, mirroring store.Repository's interface-first shape so a future swap of the
// query layer (docs/adr/0003) is an adapter change here too.
type Repository interface {
	CadenceDistribution(ctx context.Context) ([]CadenceDistributionRow, error)
	ChurnReasons(ctx context.Context) ([]ChurnReasonRow, error)
	OccurrenceForecast(ctx context.Context, from, to domain.Date) ([]ForecastRow, error)
	AudienceSegment(ctx context.Context, segment Segment) ([]SegmentRow, error)
	CohortRetention(ctx context.Context) ([]CohortRow, error)
}

// PostgresReadModel implements Repository against the views in migration 00002.
type PostgresReadModel struct{ db *sql.DB }

// New returns a Repository backed by db.
func New(db *sql.DB) *PostgresReadModel { return &PostgresReadModel{db: db} }

func (r *PostgresReadModel) CadenceDistribution(ctx context.Context) ([]CadenceDistributionRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT sku, interval_days, status, schedule_count
		FROM v_cadence_distribution
		ORDER BY sku, interval_days, status`)
	if err != nil {
		return nil, fmt.Errorf("query cadence distribution: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []CadenceDistributionRow
	for rows.Next() {
		var (
			row    CadenceDistributionRow
			status string
		)
		if err := rows.Scan(&row.SKU, &row.IntervalDays, &status, &row.ScheduleCount); err != nil {
			return nil, fmt.Errorf("scan cadence distribution row: %w", err)
		}
		row.Status = domain.ScheduleStatus(status)
		out = append(out, row)
	}
	return out, rows.Err()
}

func (r *PostgresReadModel) ChurnReasons(ctx context.Context) ([]ChurnReasonRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT reason_code, cancellation_count, first_at, last_at
		FROM v_churn_reasons
		ORDER BY reason_code`)
	if err != nil {
		return nil, fmt.Errorf("query churn reasons: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []ChurnReasonRow
	for rows.Next() {
		var (
			row             ChurnReasonRow
			firstAt, lastAt sql.NullTime
		)
		if err := rows.Scan(&row.ReasonCode, &row.CancellationCount, &firstAt, &lastAt); err != nil {
			return nil, fmt.Errorf("scan churn reason row: %w", err)
		}
		if firstAt.Valid {
			t := firstAt.Time
			row.FirstAt = &t
		}
		if lastAt.Valid {
			t := lastAt.Time
			row.LastAt = &t
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (r *PostgresReadModel) OccurrenceForecast(ctx context.Context, from, to domain.Date) ([]ForecastRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT week_start, sku, occurrence_count, unit_count
		FROM v_occurrence_forecast
		WHERE week_start >= $1 AND week_start <= $2
		ORDER BY week_start, sku`,
		from.ToTime(), to.ToTime())
	if err != nil {
		return nil, fmt.Errorf("query occurrence forecast: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []ForecastRow
	for rows.Next() {
		var (
			row  ForecastRow
			week time.Time
		)
		if err := rows.Scan(&week, &row.SKU, &row.OccurrenceCount, &row.UnitCount); err != nil {
			return nil, fmt.Errorf("scan forecast row: %w", err)
		}
		row.WeekStart = domain.DateOf(week.UTC())
		out = append(out, row)
	}
	return out, rows.Err()
}

func (r *PostgresReadModel) AudienceSegment(ctx context.Context, segment Segment) ([]SegmentRow, error) {
	if err := validateSegment(segment); err != nil {
		return nil, err
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT customer_id, schedule_id, segment, segment_since
		FROM v_audience_segments
		WHERE segment = $1
		ORDER BY customer_id`,
		string(segment))
	if err != nil {
		return nil, fmt.Errorf("query audience segment %q: %w", segment, err)
	}
	defer func() { _ = rows.Close() }()

	var out []SegmentRow
	for rows.Next() {
		var (
			row   SegmentRow
			since sql.NullTime
		)
		if err := rows.Scan(&row.CustomerID, &row.ScheduleID, &row.Segment, &since); err != nil {
			return nil, fmt.Errorf("scan segment row: %w", err)
		}
		if since.Valid {
			t := since.Time
			row.SegmentSince = &t
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (r *PostgresReadModel) CohortRetention(ctx context.Context) ([]CohortRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT cohort_month, interval_days, status, schedule_count
		FROM v_cohort_retention
		ORDER BY cohort_month, interval_days, status`)
	if err != nil {
		return nil, fmt.Errorf("query cohort retention: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []CohortRow
	for rows.Next() {
		var (
			row    CohortRow
			month  time.Time
			status string
		)
		if err := rows.Scan(&month, &row.IntervalDays, &status, &row.ScheduleCount); err != nil {
			return nil, fmt.Errorf("scan cohort row: %w", err)
		}
		row.CohortMonth = domain.DateOf(month.UTC())
		row.Status = domain.ScheduleStatus(status)
		out = append(out, row)
	}
	return out, rows.Err()
}
