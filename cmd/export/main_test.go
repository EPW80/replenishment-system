package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EPW80/replenishment-system/internal/readmodel"
	"github.com/EPW80/replenishment-system/internal/testsupport"
)

// writeView is exercised directly rather than through run/main: it's the part
// that actually does something testable, and keeping main() itself thin (flag
// parsing, os.Exit) is the usual Go shape for a CLI.
func TestWriteViewCadenceDistribution(t *testing.T) {
	db := testsupport.DB(t)
	seedSchedule(t, db, "SKU-EXPORT-TEST", 30, "active")

	var buf bytes.Buffer
	if err := writeView(context.Background(), readmodel.New(db), "cadence-distribution", "", "", "", &buf); err != nil {
		t.Fatalf("writeView: %v", err)
	}

	records, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	if len(records) < 2 {
		t.Fatalf("got %d CSV rows, want a header plus at least 1 data row", len(records))
	}
	if got, want := records[0], []string{"sku", "interval_days", "status", "schedule_count"}; !equalRows(got, want) {
		t.Errorf("header = %v, want %v", got, want)
	}

	var found bool
	for _, r := range records[1:] {
		if r[0] == "SKU-EXPORT-TEST" {
			found = true
			if r[1] != "30" || r[2] != "active" {
				t.Errorf("row = %v, want interval_days=30 status=active", r)
			}
		}
	}
	if !found {
		t.Error("seeded SKU did not appear in the export")
	}
}

func TestWriteViewChurnReasonsAlwaysHasSevenRows(t *testing.T) {
	db := testsupport.DB(t)

	var buf bytes.Buffer
	if err := writeView(context.Background(), readmodel.New(db), "churn-reasons", "", "", "", &buf); err != nil {
		t.Fatalf("writeView: %v", err)
	}

	records, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	// header + 7 reason codes, even with a freshly seeded (empty) schema
	if len(records) != 8 {
		t.Errorf("got %d rows, want 8 (header + all 7 closed-set reason codes)", len(records))
	}
}

func TestWriteViewRejectsUnknownView(t *testing.T) {
	db := testsupport.DB(t)

	var buf bytes.Buffer
	err := writeView(context.Background(), readmodel.New(db), "not-a-real-view", "", "", "", &buf)
	if err == nil {
		t.Fatal("expected an error for an unknown -view")
	}
}

func TestWriteViewSegmentsRequiresSegmentFlag(t *testing.T) {
	db := testsupport.DB(t)

	var buf bytes.Buffer
	err := writeView(context.Background(), readmodel.New(db), "segments", "", "", "", &buf)
	if err == nil {
		t.Fatal("expected an error when -segment is omitted for -view=segments")
	}
}

func TestWriteViewForecastRequiresDateRange(t *testing.T) {
	db := testsupport.DB(t)

	var buf bytes.Buffer
	if err := writeView(context.Background(), readmodel.New(db), "forecast", "", "", "", &buf); err == nil {
		t.Error("expected an error when -from/-to are omitted for -view=forecast")
	}
	if err := writeView(context.Background(), readmodel.New(db), "forecast", "", "not-a-date", "2026-12-31", &buf); err == nil {
		t.Error("expected an error for a malformed -from date")
	}
}

func seedSchedule(t *testing.T, db *sql.DB, sku string, intervalDays int, status string) {
	t.Helper()
	id := uuid.NewString()
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO schedules (id, customer_id, status, interval_days, anchor_date, timezone)
		VALUES ($1,$2,$3,$4,$5,'UTC')`,
		id, "cust_"+uuid.NewString()[:8], status, intervalDays, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("seed schedule: %v", err)
	}
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO schedule_items (id, schedule_id, sku, quantity) VALUES ($1,$2,$3,1)`,
		uuid.NewString(), id, sku); err != nil {
		t.Fatalf("seed schedule item: %v", err)
	}
}

func equalRows(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
