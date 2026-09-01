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

// ------------------------------------------------- spec §6 transition primitives

func TestUpdateScheduleStatusClearsPausedUntilOnResume(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()
	s := newSchedule(t, repo, domain.NewDate(2026, time.January, 1), 30)

	until := domain.NewDate(2026, time.March, 1)
	if err := repo.UpdateScheduleStatus(ctx, s.ID, domain.SchedulePaused, &until); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if err := repo.UpdateScheduleStatus(ctx, s.ID, domain.ScheduleActive, nil); err != nil {
		t.Fatalf("resume: %v", err)
	}

	got, err := repo.GetSchedule(ctx, s.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != domain.ScheduleActive {
		t.Errorf("status = %s, want active", got.Status)
	}
	if got.PausedUntil != nil {
		t.Errorf("paused_until = %s on an active schedule, want nil", got.PausedUntil)
	}
}

// The schema's schedules_paused_until_requires_paused CHECK forbids this combination.
// Catching it here gives the caller a usable error rather than a constraint violation.
func TestUpdateScheduleStatusRejectsPausedUntilOnActiveSchedule(t *testing.T) {
	_, repo := newTestDB(t)
	s := newSchedule(t, repo, domain.NewDate(2026, time.January, 1), 30)

	until := domain.NewDate(2026, time.March, 1)
	if err := repo.UpdateScheduleStatus(context.Background(), s.ID, domain.ScheduleActive, &until); err == nil {
		t.Fatal("paused_until was accepted on an active schedule")
	}
}

func TestUpdateScheduleCadenceValidatesInterval(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()
	s := newSchedule(t, repo, domain.NewDate(2026, time.January, 1), 30)
	anchor := domain.NewDate(2026, time.June, 1)

	for _, bad := range []int{0, 6, 181} {
		if err := repo.UpdateScheduleCadence(ctx, s.ID, bad, anchor); err == nil {
			t.Errorf("interval of %d days was accepted", bad)
		}
	}
	if err := repo.UpdateScheduleCadence(ctx, s.ID, 45, anchor); err != nil {
		t.Fatalf("valid cadence rejected: %v", err)
	}

	got, _ := repo.GetSchedule(ctx, s.ID)
	if got.IntervalDays != 45 || !got.AnchorDate.Equal(anchor) {
		t.Errorf("cadence = %d days from %s, want 45 from %s", got.IntervalDays, got.AnchorDate, anchor)
	}
}

// Spec §3: a placed order is settled. Nothing may rewrite it, including a skip that
// races an execution.
func TestUpdateOccurrenceStatusRefusesPlacedOrders(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()
	s := newSchedule(t, repo, domain.NewDate(2026, time.January, 1), 30)

	orderID := "wc_1001"
	occ := occurrence(s.ID, 1, domain.NewDate(2026, time.January, 31))
	occ.Status = domain.OccurrencePlaced
	occ.OrderID = &orderID
	if err := repo.CreateOccurrence(ctx, occ); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := repo.UpdateOccurrenceStatus(ctx, occ.ID, domain.OccurrenceSkipped); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("skipping a placed order returned %v, want ErrNotFound", err)
	}
	got, _ := repo.ListOccurrences(ctx, s.ID)
	if got[0].Status != domain.OccurrencePlaced {
		t.Errorf("status = %s, want the placed order untouched", got[0].Status)
	}
}

func TestUpdateOccurrenceDateOnlyMovesUnsettledOrders(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()
	s := newSchedule(t, repo, domain.NewDate(2026, time.January, 1), 30)

	open := occurrence(s.ID, 1, domain.NewDate(2026, time.January, 31))
	if err := repo.CreateOccurrence(ctx, open); err != nil {
		t.Fatalf("create: %v", err)
	}
	settled := occurrence(s.ID, 2, domain.NewDate(2026, time.March, 2))
	settled.Status = domain.OccurrenceSkipped
	if err := repo.CreateOccurrence(ctx, settled); err != nil {
		t.Fatalf("create: %v", err)
	}

	moved := domain.NewDate(2026, time.February, 7)
	if err := repo.UpdateOccurrenceDate(ctx, open.ID, moved); err != nil {
		t.Fatalf("defer: %v", err)
	}
	if err := repo.UpdateOccurrenceDate(ctx, settled.ID, moved); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deferring a skipped order returned %v, want ErrNotFound", err)
	}

	for _, o := range mustList(t, repo, s.ID) {
		if o.SequenceNo == 1 && !o.ScheduledFor.Equal(moved) {
			t.Errorf("open occurrence at %s, want %s", o.ScheduledFor, moved)
		}
		if o.SequenceNo == 2 && !o.ScheduledFor.Equal(settled.ScheduledFor) {
			t.Errorf("settled occurrence moved to %s", o.ScheduledFor)
		}
	}
}

// Spec §5: a cadence change rewrites planned occurrences but leaves pending ones
// alone, because a pending occurrence has already had its pre-billing notice sent.
func TestCancelPlannedVersusUnexecutedOccurrences(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()

	build := func() (domain.Schedule, domain.Occurrence, domain.Occurrence) {
		s := newSchedule(t, repo, domain.NewDate(2026, time.January, 1), 30)
		planned := occurrence(s.ID, 1, domain.NewDate(2026, time.January, 31))
		if err := repo.CreateOccurrence(ctx, planned); err != nil {
			t.Fatalf("create planned: %v", err)
		}
		pending := occurrence(s.ID, 2, domain.NewDate(2026, time.March, 2))
		if err := repo.CreateOccurrence(ctx, pending); err != nil {
			t.Fatalf("create pending: %v", err)
		}
		if err := repo.UpdateOccurrenceStatus(ctx, pending.ID, domain.OccurrencePending); err != nil {
			t.Fatalf("arm: %v", err)
		}
		return s, planned, pending
	}

	// CancelPlannedOccurrences spares the pending one.
	s, _, pending := build()
	n, err := repo.CancelPlannedOccurrences(ctx, s.ID)
	if err != nil {
		t.Fatalf("cancel planned: %v", err)
	}
	if n != 1 {
		t.Errorf("canceled %d, want only the planned one", n)
	}
	for _, o := range mustList(t, repo, s.ID) {
		if o.ID == pending.ID && o.Status != domain.OccurrencePending {
			t.Errorf("pending occurrence became %s; its pre-billing notice was already sent", o.Status)
		}
	}

	// CancelUnexecutedOccurrences takes both — that is pause and cancel.
	s2, _, _ := build()
	n, err = repo.CancelUnexecutedOccurrences(ctx, s2.ID)
	if err != nil {
		t.Fatalf("cancel unexecuted: %v", err)
	}
	if n != 2 {
		t.Errorf("canceled %d, want both", n)
	}
	for _, o := range mustList(t, repo, s2.ID) {
		if o.Status != domain.OccurrenceCanceled {
			t.Errorf("occurrence %d left in %s", o.SequenceNo, o.Status)
		}
	}
}

// "Next" means the order arriving soonest, not the lowest sequence number. After a
// defer the two disagree, and the customer means the date.
func TestNextActionableOccurrenceOrdersByDate(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()
	s := newSchedule(t, repo, domain.NewDate(2026, time.January, 1), 30)

	first := occurrence(s.ID, 1, domain.NewDate(2026, time.January, 31))
	second := occurrence(s.ID, 2, domain.NewDate(2026, time.March, 2))
	for _, o := range []domain.Occurrence{first, second} {
		if err := repo.CreateOccurrence(ctx, o); err != nil {
			t.Fatalf("create: %v", err)
		}
	}

	// Push the lower sequence number past the higher one.
	if err := repo.UpdateOccurrenceDate(ctx, first.ID, domain.NewDate(2026, time.April, 1)); err != nil {
		t.Fatalf("defer: %v", err)
	}

	got, err := repo.NextActionableOccurrence(ctx, s.ID)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if got.SequenceNo != 2 {
		t.Errorf("next occurrence is sequence %d, want 2 — the soonest date, not the lowest number", got.SequenceNo)
	}
}

func TestNextActionableOccurrenceSkipsSettledOnes(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()
	s := newSchedule(t, repo, domain.NewDate(2026, time.January, 1), 30)

	if _, err := repo.NextActionableOccurrence(ctx, s.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("empty schedule returned %v, want ErrNotFound", err)
	}

	settled := occurrence(s.ID, 1, domain.NewDate(2026, time.January, 31))
	settled.Status = domain.OccurrenceSkipped
	if err := repo.CreateOccurrence(ctx, settled); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := repo.NextActionableOccurrence(ctx, s.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a schedule with only settled orders returned %v, want ErrNotFound", err)
	}
}

func TestLastPlacedOccurrence(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()
	s := newSchedule(t, repo, domain.NewDate(2026, time.January, 1), 30)

	if _, err := repo.LastPlacedOccurrence(ctx, s.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a schedule that never shipped returned %v, want ErrNotFound", err)
	}

	for i, date := range []domain.Date{
		domain.NewDate(2026, time.January, 31),
		domain.NewDate(2026, time.March, 2),
	} {
		o := occurrence(s.ID, i+1, date)
		o.Status = domain.OccurrencePlaced
		if err := repo.CreateOccurrence(ctx, o); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	// A planned occurrence further out must not win.
	if err := repo.CreateOccurrence(ctx, occurrence(s.ID, 3, domain.NewDate(2026, time.April, 1))); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := repo.LastPlacedOccurrence(ctx, s.ID)
	if err != nil {
		t.Fatalf("last placed: %v", err)
	}
	if want := domain.NewDate(2026, time.March, 2); !got.ScheduledFor.Equal(want) {
		t.Errorf("last placed = %s, want %s", got.ScheduledFor, want)
	}
}

func TestLatestScheduledDate(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()
	s := newSchedule(t, repo, domain.NewDate(2026, time.January, 1), 30)

	got, err := repo.LatestScheduledDate(ctx, s.ID)
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if got != nil {
		t.Fatalf("latest = %s on an empty schedule, want nil", got)
	}

	furthest := domain.NewDate(2026, time.April, 1)
	for i, date := range []domain.Date{domain.NewDate(2026, time.January, 31), furthest} {
		if err := repo.CreateOccurrence(ctx, occurrence(s.ID, i+1, date)); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	// A settled occurrence beyond the horizon must not extend it.
	settled := occurrence(s.ID, 3, domain.NewDate(2027, time.January, 1))
	settled.Status = domain.OccurrenceCanceled
	if err := repo.CreateOccurrence(ctx, settled); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err = repo.LatestScheduledDate(ctx, s.ID)
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if got == nil || !got.Equal(furthest) {
		t.Errorf("latest = %v, want %s", got, furthest)
	}
}

// ------------------------------------------------------------------ transactions

// A transition and the event recording it commit together or not at all. An event log
// that disagrees with the state it describes is not an audit trail.
func TestInTxRollsBackEverythingOnError(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()
	s := newSchedule(t, repo, domain.NewDate(2026, time.January, 1), 30)

	sentinel := errors.New("transition failed partway")
	err := repo.InTx(ctx, func(tx store.Repository) error {
		if err := tx.UpdateScheduleStatus(ctx, s.ID, domain.SchedulePaused, nil); err != nil {
			return err
		}
		if err := tx.AppendEvent(ctx, domain.ScheduleEvent{
			ScheduleID: s.ID,
			EventType:  domain.EventSchedulePaused,
			Actor:      domain.ActorCustomer,
		}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("InTx returned %v, want the caller's error", err)
	}

	got, err := repo.GetSchedule(ctx, s.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != domain.ScheduleActive {
		t.Errorf("status = %s after a rolled-back transition, want active", got.Status)
	}
	events, err := repo.ListEvents(ctx, s.ID)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("%d events survived the rollback — the log now describes a state that "+
			"never existed", len(events))
	}
}

func TestInTxCommitsOnSuccess(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()
	s := newSchedule(t, repo, domain.NewDate(2026, time.January, 1), 30)

	err := repo.InTx(ctx, func(tx store.Repository) error {
		if err := tx.UpdateScheduleStatus(ctx, s.ID, domain.SchedulePaused, nil); err != nil {
			return err
		}
		return tx.AppendEvent(ctx, domain.ScheduleEvent{
			ScheduleID: s.ID,
			EventType:  domain.EventSchedulePaused,
			Actor:      domain.ActorCustomer,
		})
	})
	if err != nil {
		t.Fatalf("InTx: %v", err)
	}

	got, _ := repo.GetSchedule(ctx, s.ID)
	if got.Status != domain.SchedulePaused {
		t.Errorf("status = %s, want paused", got.Status)
	}
	if events, _ := repo.ListEvents(ctx, s.ID); len(events) != 1 {
		t.Errorf("got %d events, want 1", len(events))
	}
}

// A service method composed of others must still commit atomically, so a nested InTx
// joins the outer transaction rather than opening a second one that can half-commit.
func TestInTxNestsIntoTheOuterTransaction(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()
	s := newSchedule(t, repo, domain.NewDate(2026, time.January, 1), 30)

	sentinel := errors.New("outer failed")
	err := repo.InTx(ctx, func(tx store.Repository) error {
		if err := tx.InTx(ctx, func(inner store.Repository) error {
			return inner.UpdateScheduleStatus(ctx, s.ID, domain.SchedulePaused, nil)
		}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("InTx returned %v, want the caller's error", err)
	}

	got, _ := repo.GetSchedule(ctx, s.ID)
	if got.Status != domain.ScheduleActive {
		t.Errorf("status = %s — the inner transaction committed independently of the outer one", got.Status)
	}
}

// CreateSchedule writes the schedule and its items in one transaction; a bad item must
// leave no half-built schedule behind.
func TestCreateScheduleIsAtomic(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()

	s := domain.Schedule{
		ID:           uuid.NewString(),
		CustomerID:   "cust_" + uuid.NewString()[:8],
		Status:       domain.ScheduleActive,
		IntervalDays: 30,
		AnchorDate:   domain.NewDate(2026, time.January, 1),
		Timezone:     "UTC",
	}
	items := []domain.ScheduleItem{
		{ID: uuid.NewString(), ScheduleID: s.ID, SKU: "SKU-001", Quantity: 1},
		{ID: uuid.NewString(), ScheduleID: s.ID, SKU: "SKU-002", Quantity: 0}, // violates the CHECK
	}
	if err := repo.CreateSchedule(ctx, s, items); err == nil {
		t.Fatal("a zero-quantity item was accepted")
	}
	if _, err := repo.GetSchedule(ctx, s.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("get returned %v, want ErrNotFound — a half-built schedule survived", err)
	}
}

// UnnotifiedEvents/RecordNotification back the Phase 4 dispatcher's outbox query —
// events of the requested types that haven't been recorded in notification_log yet.
func TestUnnotifiedEventsExcludesRecorded(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()

	s := newSchedule(t, repo, domain.NewDate(2026, time.January, 1), 30)
	if err := repo.AppendEvent(ctx, domain.ScheduleEvent{
		ScheduleID: s.ID, EventType: domain.EventScheduleCreated, Actor: domain.ActorCustomer,
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := repo.AppendEvent(ctx, domain.ScheduleEvent{
		ScheduleID: s.ID, EventType: domain.EventSchedulePaused, Actor: domain.ActorCustomer,
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	events, err := repo.UnnotifiedEvents(ctx, []string{domain.EventScheduleCreated, domain.EventSchedulePaused})
	if err != nil {
		t.Fatalf("UnnotifiedEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d unnotified events, want 2", len(events))
	}

	// Record one. It must disappear from the next query; the other stays.
	if err := repo.RecordNotification(ctx, events[0].ID, "schedule_created"); err != nil {
		t.Fatalf("RecordNotification: %v", err)
	}
	remaining, err := repo.UnnotifiedEvents(ctx, []string{domain.EventScheduleCreated, domain.EventSchedulePaused})
	if err != nil {
		t.Fatalf("UnnotifiedEvents (2nd): %v", err)
	}
	if len(remaining) != 1 || remaining[0].ID != events[1].ID {
		t.Fatalf("got %+v, want only event %d remaining", remaining, events[1].ID)
	}
}

// The event-type filter must actually filter — an event type the caller didn't ask
// about (skip, defer, cadence change) must never trigger a notification meant only
// for the four spec §7 sends this phase implements.
func TestUnnotifiedEventsFiltersByType(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()

	s := newSchedule(t, repo, domain.NewDate(2026, time.January, 1), 30)
	if err := repo.AppendEvent(ctx, domain.ScheduleEvent{
		ScheduleID: s.ID, EventType: domain.EventScheduleCadenceChanged, Actor: domain.ActorCustomer,
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	events, err := repo.UnnotifiedEvents(ctx, []string{domain.EventScheduleCreated})
	if err != nil {
		t.Fatalf("UnnotifiedEvents: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("got %d events, want 0 — cadence_changed was not in the requested type list", len(events))
	}
}

// A duplicate RecordNotification call must fail loudly, not silently double-count or
// panic — the dispatcher relies on this to be safe against a re-run racing itself.
func TestRecordNotificationRejectsDuplicate(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()

	s := newSchedule(t, repo, domain.NewDate(2026, time.January, 1), 30)
	if err := repo.AppendEvent(ctx, domain.ScheduleEvent{
		ScheduleID: s.ID, EventType: domain.EventScheduleCreated, Actor: domain.ActorCustomer,
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	events, err := repo.UnnotifiedEvents(ctx, []string{domain.EventScheduleCreated})
	if err != nil || len(events) != 1 {
		t.Fatalf("setup: events=%+v err=%v", events, err)
	}

	if err := repo.RecordNotification(ctx, events[0].ID, "schedule_created"); err != nil {
		t.Fatalf("first RecordNotification: %v", err)
	}
	if err := repo.RecordNotification(ctx, events[0].ID, "schedule_created"); !errors.Is(err, store.ErrDuplicateNotification) {
		t.Fatalf("second RecordNotification error = %v, want ErrDuplicateNotification", err)
	}
}

func mustList(t *testing.T, repo *store.PostgresRepository, scheduleID string) []domain.Occurrence {
	t.Helper()
	got, err := repo.ListOccurrences(context.Background(), scheduleID)
	if err != nil {
		t.Fatalf("list occurrences: %v", err)
	}
	return got
}
