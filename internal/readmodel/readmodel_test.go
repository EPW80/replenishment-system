package readmodel_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EPW80/replenishment-system/internal/domain"
	"github.com/EPW80/replenishment-system/internal/readmodel"
	"github.com/EPW80/replenishment-system/internal/testsupport"
)

func setup(t *testing.T) (*sql.DB, *readmodel.PostgresReadModel) {
	t.Helper()
	db := testsupport.DB(t)
	return db, readmodel.New(db)
}

// insertSchedule writes a schedule directly via SQL rather than through
// store.Repository: several fixtures here need a status ('failed') or an event
// timestamp in the past that no domain transition produces, and store_test.go
// already sets the precedent of raw SQL for exactly that gap.
func insertSchedule(t *testing.T, db *sql.DB, id, customerID, status string, intervalDays int, createdAt time.Time) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO schedules (id, customer_id, customer_email, status, interval_days, anchor_date, timezone, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,'UTC',$7,$7)`,
		id, customerID, customerID+"@example.com", status, intervalDays, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), createdAt)
	if err != nil {
		t.Fatalf("insert schedule: %v", err)
	}
}

func insertItem(t *testing.T, db *sql.DB, scheduleID, sku string, quantity int) {
	t.Helper()
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO schedule_items (id, schedule_id, sku, quantity) VALUES ($1,$2,$3,$4)`,
		uuid.NewString(), scheduleID, sku, quantity)
	if err != nil {
		t.Fatalf("insert item: %v", err)
	}
}

func insertOccurrence(t *testing.T, db *sql.DB, scheduleID string, seq int, scheduledFor time.Time, status string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO occurrences (id, schedule_id, sequence_no, scheduled_for, status, idempotency_key)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		uuid.NewString(), scheduleID, seq, scheduledFor, status, fmt.Sprintf("%s:%d", scheduleID, seq))
	if err != nil {
		t.Fatalf("insert occurrence: %v", err)
	}
}

func insertEvent(t *testing.T, db *sql.DB, scheduleID, eventType, reasonCode string, createdAt time.Time) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO schedule_events (schedule_id, event_type, actor, reason_code, payload, created_at)
		VALUES ($1,$2,'customer',$3,'{}',$4)`,
		scheduleID, eventType, sql.NullString{String: reasonCode, Valid: reasonCode != ""}, createdAt)
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}
}

func TestCadenceDistribution(t *testing.T) {
	db, rm := setup(t)
	now := time.Now()

	a, b, c := uuid.NewString(), uuid.NewString(), uuid.NewString()
	insertSchedule(t, db, a, "cust_a", "active", 30, now)
	insertSchedule(t, db, b, "cust_b", "active", 30, now)
	insertSchedule(t, db, c, "cust_c", "canceled", 60, now)
	insertItem(t, db, a, "SKU-A", 1)
	insertItem(t, db, b, "SKU-A", 1)
	insertItem(t, db, c, "SKU-B", 1)

	rows, err := rm.CadenceDistribution(context.Background())
	if err != nil {
		t.Fatalf("CadenceDistribution: %v", err)
	}

	var got map[string]int = map[string]int{}
	for _, r := range rows {
		if r.SKU == "SKU-A" || r.SKU == "SKU-B" {
			got[r.SKU+"/"+string(r.Status)] = r.ScheduleCount
		}
	}
	if got["SKU-A/active"] != 2 {
		t.Errorf("SKU-A/active = %d, want 2", got["SKU-A/active"])
	}
	if got["SKU-B/canceled"] != 1 {
		t.Errorf("SKU-B/canceled = %d, want 1", got["SKU-B/canceled"])
	}
}

// Every reason code in the closed set must appear, even at zero — a dashboard must
// never have to guess whether an absent code means "never happened" or "the view
// doesn't know about it."
func TestChurnReasonsIncludesZeroCounts(t *testing.T) {
	db, rm := setup(t)
	now := time.Now()

	s := uuid.NewString()
	insertSchedule(t, db, s, "cust_"+uuid.NewString()[:8], "canceled", 30, now)
	insertEvent(t, db, s, "schedule.canceled", "too_frequent", now)

	rows, err := rm.ChurnReasons(context.Background())
	if err != nil {
		t.Fatalf("ChurnReasons: %v", err)
	}

	if len(rows) != len(domain.CancellationReasons) {
		t.Fatalf("got %d reason codes, want all %d from the closed set", len(rows), len(domain.CancellationReasons))
	}

	byCode := map[string]readmodel.ChurnReasonRow{}
	for _, r := range rows {
		byCode[r.ReasonCode] = r
	}
	if got := byCode["too_frequent"]; got.CancellationCount == 0 {
		t.Error("too_frequent should have at least 1 cancellation")
	}
	if got := byCode["payment_issue"]; got.CancellationCount != 0 || got.FirstAt != nil || got.LastAt != nil {
		t.Errorf("payment_issue (never used here) should be a real zero row, got %+v", got)
	}
}

func TestOccurrenceForecastCountsOnlyPlanned(t *testing.T) {
	db, rm := setup(t)
	now := time.Now()

	s := uuid.NewString()
	insertSchedule(t, db, s, "cust_"+uuid.NewString()[:8], "active", 30, now)
	insertItem(t, db, s, "SKU-Z", 3)

	week := time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC) // a Monday
	insertOccurrence(t, db, s, 1, week, "planned")
	insertOccurrence(t, db, s, 2, week.AddDate(0, 0, 1), "skipped") // must not be counted
	insertOccurrence(t, db, s, 3, week.AddDate(0, 0, 2), "canceled")

	rows, err := rm.OccurrenceForecast(context.Background(),
		domain.NewDate(2026, time.June, 1), domain.NewDate(2026, time.June, 30))
	if err != nil {
		t.Fatalf("OccurrenceForecast: %v", err)
	}

	var found bool
	for _, r := range rows {
		if r.SKU != "SKU-Z" {
			continue
		}
		found = true
		if r.OccurrenceCount != 1 {
			t.Errorf("occurrence_count = %d, want 1 (only the planned one)", r.OccurrenceCount)
		}
		if r.UnitCount != 3 {
			t.Errorf("unit_count = %d, want 3", r.UnitCount)
		}
	}
	if !found {
		t.Fatal("SKU-Z did not appear in the forecast")
	}
}

func TestOccurrenceForecastRangeFilter(t *testing.T) {
	db, rm := setup(t)
	now := time.Now()

	s := uuid.NewString()
	insertSchedule(t, db, s, "cust_"+uuid.NewString()[:8], "active", 30, now)
	insertItem(t, db, s, "SKU-OUT", 1)
	insertOccurrence(t, db, s, 1, time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), "planned")

	rows, err := rm.OccurrenceForecast(context.Background(),
		domain.NewDate(2026, time.June, 1), domain.NewDate(2026, time.June, 30))
	if err != nil {
		t.Fatalf("OccurrenceForecast: %v", err)
	}
	for _, r := range rows {
		if r.SKU == "SKU-OUT" {
			t.Errorf("occurrence far outside the requested range leaked in: %+v", r)
		}
	}
}

// segment_since must come from the event log, not schedules.updated_at: an
// unrelated write touching updated_at must not be able to shift when a segment
// "started".
func TestAudienceSegmentPausedSincePinnedToEvent(t *testing.T) {
	db, rm := setup(t)
	now := time.Now()

	s := uuid.NewString()
	insertSchedule(t, db, s, "cust_"+uuid.NewString()[:8], "paused", 30, now)
	pausedAt := now.Add(-48 * time.Hour)
	insertEvent(t, db, s, "schedule.paused", "", pausedAt)

	// An unrelated write to updated_at. If segment_since read that column instead
	// of the event log, this would corrupt the reported timestamp.
	if _, err := db.ExecContext(context.Background(),
		`UPDATE schedules SET discount_pct = 5, updated_at = now() WHERE id = $1`, s); err != nil {
		t.Fatalf("unrelated update: %v", err)
	}

	rows, err := rm.AudienceSegment(context.Background(), readmodel.SegmentPaused)
	if err != nil {
		t.Fatalf("AudienceSegment: %v", err)
	}

	var found bool
	for _, r := range rows {
		if r.ScheduleID != s {
			continue
		}
		found = true
		if r.SegmentSince == nil {
			t.Fatal("segment_since is nil, want the paused event's timestamp")
		}
		if diff := r.SegmentSince.Sub(pausedAt); diff < -time.Second || diff > time.Second {
			t.Errorf("segment_since = %s, want ~%s (the event time, not updated_at)", r.SegmentSince, pausedAt)
		}
	}
	if !found {
		t.Fatal("paused schedule missing from the segment")
	}
}

func TestAudienceSegmentCanceledWithin90dExcludesOlder(t *testing.T) {
	db, rm := setup(t)
	now := time.Now()

	recent := uuid.NewString()
	insertSchedule(t, db, recent, "cust_"+uuid.NewString()[:8], "canceled", 30, now)
	insertEvent(t, db, recent, "schedule.canceled", "other", now.Add(-5*24*time.Hour))

	old := uuid.NewString()
	insertSchedule(t, db, old, "cust_"+uuid.NewString()[:8], "canceled", 30, now)
	insertEvent(t, db, old, "schedule.canceled", "other", now.Add(-200*24*time.Hour))

	rows, err := rm.AudienceSegment(context.Background(), readmodel.SegmentCanceledWithin90d)
	if err != nil {
		t.Fatalf("AudienceSegment: %v", err)
	}

	var sawRecent, sawOld bool
	for _, r := range rows {
		if r.ScheduleID == recent {
			sawRecent = true
		}
		if r.ScheduleID == old {
			sawOld = true
		}
	}
	if !sawRecent {
		t.Error("schedule canceled 5 days ago missing from canceled_within_90d")
	}
	if sawOld {
		t.Error("schedule canceled 200 days ago present in canceled_within_90d")
	}
}

// Nothing produces a 'failed' schedule yet — the segment must return empty, not
// error, and this must stay true until Phase 4 exists.
func TestAudienceSegmentFailedIsEmptyNotError(t *testing.T) {
	_, rm := setup(t)

	rows, err := rm.AudienceSegment(context.Background(), readmodel.SegmentFailed)
	if err != nil {
		t.Fatalf("AudienceSegment(failed): %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("got %d rows for a segment nothing produces yet, want 0", len(rows))
	}
}

// A typo'd segment name must fail loudly, not silently return an empty slice --
// SegmentFailed legitimately returns zero rows today, and that has to stay
// distinguishable from "this segment name doesn't exist."
func TestAudienceSegmentRejectsUnknownSegment(t *testing.T) {
	_, rm := setup(t)

	_, err := rm.AudienceSegment(context.Background(), readmodel.Segment("typo_segment"))
	if !errors.Is(err, readmodel.ErrInvalidSegment) {
		t.Fatalf("error = %v, want ErrInvalidSegment", err)
	}
}

func TestCohortRetention(t *testing.T) {
	db, rm := setup(t)

	s := uuid.NewString()
	insertSchedule(t, db, s, "cust_"+uuid.NewString()[:8], "active", 45,
		time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC))

	rows, err := rm.CohortRetention(context.Background())
	if err != nil {
		t.Fatalf("CohortRetention: %v", err)
	}

	var found bool
	for _, r := range rows {
		if r.IntervalDays == 45 && r.Status == domain.ScheduleActive &&
			r.CohortMonth.Equal(domain.NewDate(2026, time.March, 1)) {
			found = true
			if r.ScheduleCount < 1 {
				t.Errorf("schedule_count = %d, want at least 1", r.ScheduleCount)
			}
		}
	}
	if !found {
		t.Fatal("expected cohort (2026-03, 45 days, active) not found")
	}
}
