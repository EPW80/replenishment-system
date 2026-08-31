package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EPW80/replenishment-system/internal/domain"
	"github.com/EPW80/replenishment-system/internal/store"
)

// The property spec §3 calls "the whole safety story for order creation": the same
// key twice produces one occurrence, always. A retry, a duplicate queue delivery, or
// a redeploy mid-run must never produce a second charge.
func TestIdempotencyKeyPreventsDuplicates(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()

	s := newSchedule(t, repo, domain.NewDate(2026, time.January, 1), 30)
	occ := occurrence(s.ID, 1, domain.NewDate(2026, time.January, 31))

	if err := repo.CreateOccurrence(ctx, occ); err != nil {
		t.Fatalf("first insert: %v", err)
	}

	// Same key, different row id — what a retry looks like.
	retry := occ
	retry.ID = uuid.NewString()
	err := repo.CreateOccurrence(ctx, retry)
	if !errors.Is(err, store.ErrDuplicateOccurrence) {
		t.Fatalf("second insert error = %v, want ErrDuplicateOccurrence", err)
	}

	got, err := repo.ListOccurrences(ctx, s.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d occurrences, want exactly 1 — a duplicate here is a duplicate charge", len(got))
	}
}

// The same sequence number must not be reusable under a different key either.
func TestSequenceNumberIsUniquePerSchedule(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()

	s := newSchedule(t, repo, domain.NewDate(2026, time.January, 1), 30)

	first := occurrence(s.ID, 1, domain.NewDate(2026, time.January, 31))
	if err := repo.CreateOccurrence(ctx, first); err != nil {
		t.Fatalf("first insert: %v", err)
	}

	second := occurrence(s.ID, 1, domain.NewDate(2026, time.February, 15))
	second.IdempotencyKey = "deliberately-different-key"
	if err := repo.CreateOccurrence(ctx, second); !errors.Is(err, store.ErrDuplicateOccurrence) {
		t.Fatalf("error = %v, want ErrDuplicateOccurrence", err)
	}
}

// Two schedules may hold the same sequence number; the key is scoped per schedule.
func TestSequenceNumbersAreScopedPerSchedule(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()

	a := newSchedule(t, repo, domain.NewDate(2026, time.January, 1), 30)
	b := newSchedule(t, repo, domain.NewDate(2026, time.January, 1), 30)

	for _, s := range []domain.Schedule{a, b} {
		if err := repo.CreateOccurrence(ctx, occurrence(s.ID, 1, domain.NewDate(2026, time.January, 31))); err != nil {
			t.Fatalf("schedule %s: %v", s.ID, err)
		}
	}
}

func TestScheduleRoundTrip(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()

	anchor := domain.NewDate(2026, time.March, 15)
	s := newSchedule(t, repo, anchor, 45)

	got, err := repo.GetSchedule(ctx, s.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.AnchorDate.Equal(anchor) {
		t.Errorf("anchor = %s, want %s", got.AnchorDate, anchor)
	}
	if got.IntervalDays != 45 || got.Status != domain.ScheduleActive {
		t.Errorf("unexpected schedule: %+v", got)
	}
	if got.Timezone != "America/Los_Angeles" {
		t.Errorf("timezone = %q", got.Timezone)
	}

	items, err := repo.ListScheduleItems(ctx, s.ID)
	if err != nil {
		t.Fatalf("list items: %v", err)
	}
	if len(items) != 1 || items[0].SKU != "SKU-001" {
		t.Errorf("unexpected items: %+v", items)
	}

	if _, err := repo.GetSchedule(ctx, uuid.NewString()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing schedule error = %v, want ErrNotFound", err)
	}
}

// The schema enforces the spec §3 range, not only the domain layer — a value that
// reaches the database has already escaped the domain.
func TestScheduleRejectsOutOfRangeInterval(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()

	for _, interval := range []int{6, 181, 0, -1} {
		s := domain.Schedule{
			ID: uuid.NewString(), CustomerID: "c1", Status: domain.ScheduleActive,
			IntervalDays: interval, AnchorDate: domain.NewDate(2026, time.January, 1),
			Timezone: "UTC",
		}
		if err := repo.CreateSchedule(ctx, s, nil); err == nil {
			t.Errorf("interval_days=%d was accepted; spec §3 allows 7-180 only", interval)
		}
	}
}

func TestScheduleRejectsInvalidTimezone(t *testing.T) {
	_, repo := newTestDB(t)

	s := domain.Schedule{
		ID: uuid.NewString(), CustomerID: "c1", Status: domain.ScheduleActive,
		IntervalDays: 30, AnchorDate: domain.NewDate(2026, time.January, 1),
		Timezone: "Mars/Olympus_Mons",
	}
	if err := repo.CreateSchedule(context.Background(), s, nil); err == nil {
		t.Error("invalid IANA timezone was accepted")
	}
}

// The event log is append-only by construction: the Repository interface exposes no
// update or delete. This test pins the write-and-read path and the ordering the
// spec §8 read models depend on.
func TestEventLogIsAppendOnly(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()

	s := newSchedule(t, repo, domain.NewDate(2026, time.January, 1), 30)

	reason := "customer_request"
	for _, e := range []domain.ScheduleEvent{
		{ScheduleID: s.ID, EventType: domain.EventScheduleCreated, Actor: domain.ActorCustomer},
		{ScheduleID: s.ID, EventType: domain.EventOccurrencePlanned, Actor: domain.ActorSystem,
			Payload: []byte(`{"sequence_no":1}`)},
		{ScheduleID: s.ID, EventType: "schedule.paused", Actor: domain.ActorCustomer, ReasonCode: &reason},
	} {
		if err := repo.AppendEvent(ctx, e); err != nil {
			t.Fatalf("append %s: %v", e.EventType, err)
		}
	}

	events, err := repo.ListEvents(ctx, s.ID)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
	// Ordered by id, so the log reads in the order things happened.
	for i := 1; i < len(events); i++ {
		if events[i].ID <= events[i-1].ID {
			t.Errorf("events out of order at %d", i)
		}
	}
	if events[2].ReasonCode == nil || *events[2].ReasonCode != reason {
		t.Errorf("reason code not round-tripped: %+v", events[2].ReasonCode)
	}
	// Churn analysis (spec §8) depends on the actor being recorded.
	if events[0].Actor != domain.ActorCustomer || events[1].Actor != domain.ActorSystem {
		t.Errorf("actors not round-tripped: %v, %v", events[0].Actor, events[1].Actor)
	}
}

// The schema refuses an order reference on an occurrence that was never placed --
// an order_id on a skipped occurrence would corrupt reconciliation.
func TestOrderIDRequiresPlacedStatus(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()

	s := newSchedule(t, repo, domain.NewDate(2026, time.January, 1), 30)
	occ := occurrence(s.ID, 1, domain.NewDate(2026, time.January, 31))
	orderID := "wc_12345"
	occ.OrderID = &orderID // still status 'planned'

	if err := repo.CreateOccurrence(ctx, occ); err == nil {
		t.Error("an order_id on a non-placed occurrence was accepted")
	}
}

func TestListActiveSchedulesExcludesInactive(t *testing.T) {
	db, repo := newTestDB(t)
	ctx := context.Background()

	active := newSchedule(t, repo, domain.NewDate(2026, time.January, 1), 30)
	paused := newSchedule(t, repo, domain.NewDate(2026, time.January, 1), 30)
	if _, err := db.ExecContext(ctx,
		`UPDATE schedules SET status = 'paused' WHERE id = $1`, paused.ID); err != nil {
		t.Fatalf("pause: %v", err)
	}

	got, err := repo.ListActiveSchedules(ctx)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}

	var sawActive, sawPaused bool
	for _, s := range got {
		if s.ID == active.ID {
			sawActive = true
		}
		if s.ID == paused.ID {
			sawPaused = true
		}
	}
	if !sawActive {
		t.Error("active schedule missing from ListActiveSchedules")
	}
	if sawPaused {
		t.Error("paused schedule returned by ListActiveSchedules")
	}
}

func TestUpdateScheduleNextRun(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()

	s := newSchedule(t, repo, domain.NewDate(2026, time.January, 1), 30)
	next := domain.NewDate(2026, time.January, 31)

	if err := repo.UpdateScheduleNextRun(ctx, s.ID, &next); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := repo.GetSchedule(ctx, s.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.NextRunDate == nil || !got.NextRunDate.Equal(next) {
		t.Fatalf("next_run_date = %v, want %s", got.NextRunDate, next)
	}

	if err := repo.UpdateScheduleNextRun(ctx, s.ID, nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, _ = repo.GetSchedule(ctx, s.ID)
	if got.NextRunDate != nil {
		t.Errorf("next_run_date = %v, want nil", got.NextRunDate)
	}

	if err := repo.UpdateScheduleNextRun(ctx, uuid.NewString(), &next); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func TestMaxSequenceNo(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()

	s := newSchedule(t, repo, domain.NewDate(2026, time.January, 1), 30)

	if n, err := repo.MaxSequenceNo(ctx, s.ID); err != nil || n != 0 {
		t.Fatalf("empty schedule: n=%d err=%v, want 0", n, err)
	}
	for _, seq := range []int{1, 2, 5} {
		date, _ := domain.OccurrenceDate(s.AnchorDate, 30, seq)
		if err := repo.CreateOccurrence(ctx, occurrence(s.ID, seq, date)); err != nil {
			t.Fatalf("seq %d: %v", seq, err)
		}
	}
	if n, err := repo.MaxSequenceNo(ctx, s.ID); err != nil || n != 5 {
		t.Fatalf("n=%d err=%v, want 5", n, err)
	}
}
