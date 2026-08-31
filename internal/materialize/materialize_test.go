package materialize_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/EPW80/replenishment-system/internal/domain"
	"github.com/EPW80/replenishment-system/internal/materialize"
	"github.com/EPW80/replenishment-system/internal/store"
	"github.com/EPW80/replenishment-system/internal/testsupport"
	"github.com/google/uuid"
)

func setup(t *testing.T) (*sql.DB, *store.PostgresRepository) {
	t.Helper()
	db := testsupport.DB(t)
	return db, store.New(db)
}

func newSchedule(t *testing.T, _ *sql.DB, repo *store.PostgresRepository, anchor domain.Date, interval int) domain.Schedule {
	t.Helper()
	s := domain.Schedule{
		ID:           uuid.NewString(),
		CustomerID:   "cust_" + uuid.NewString()[:8],
		Status:       domain.ScheduleActive,
		IntervalDays: interval,
		AnchorDate:   anchor,
		Timezone:     "America/Los_Angeles",
	}
	if err := repo.CreateSchedule(context.Background(), s, nil); err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	return s
}

// Spec §5 step 1: maintain a rolling horizon of planned occurrences, default 3.
func TestRunCreatesHorizon(t *testing.T) {
	db, repo := setup(t)
	ctx := context.Background()

	anchor := domain.NewDate(2026, time.January, 1)
	s := newSchedule(t, db, repo, anchor, 30)
	today := domain.NewDate(2026, time.January, 15)

	m := materialize.New(repo, 3, nil)
	created, dupes, err := m.Run(ctx, s, today)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if created != 3 || dupes != 0 {
		t.Fatalf("created=%d dupes=%d, want 3 and 0", created, dupes)
	}

	got, err := repo.ListOccurrences(ctx, s.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d occurrences, want 3", len(got))
	}

	// Every date derives from the anchor, so they are exact multiples of the
	// interval away from it -- not accumulated from each other.
	for i, o := range got {
		want, err := domain.OccurrenceDate(anchor, 30, o.SequenceNo)
		if err != nil {
			t.Fatalf("expected date: %v", err)
		}
		if !o.ScheduledFor.Equal(want) {
			t.Errorf("occurrence %d scheduled for %s, want %s", i, o.ScheduledFor, want)
		}
		if !o.ScheduledFor.After(today) {
			t.Errorf("occurrence %d is not in the future: %s", i, o.ScheduledFor)
		}
		if o.Status != domain.OccurrencePlanned {
			t.Errorf("occurrence %d status = %s, want planned", i, o.Status)
		}
		if want := domain.IdempotencyKey(s.ID, o.SequenceNo); o.IdempotencyKey != want {
			t.Errorf("idempotency key = %q, want %q", o.IdempotencyKey, want)
		}
	}
}

// Running twice must create nothing the second time. The nightly job, a retry and a
// manual invocation can all overlap, and a duplicate occurrence carries a duplicate
// idempotency key into order creation.
func TestRunIsIdempotent(t *testing.T) {
	db, repo := setup(t)
	ctx := context.Background()

	s := newSchedule(t, db, repo, domain.NewDate(2026, time.January, 1), 30)
	today := domain.NewDate(2026, time.January, 15)
	m := materialize.New(repo, 3, nil)

	if _, _, err := m.Run(ctx, s, today); err != nil {
		t.Fatalf("first run: %v", err)
	}
	before, _ := repo.ListOccurrences(ctx, s.ID)

	for i := 0; i < 3; i++ {
		created, _, err := m.Run(ctx, s, today)
		if err != nil {
			t.Fatalf("rerun %d: %v", i, err)
		}
		if created != 0 {
			t.Errorf("rerun %d created %d occurrences, want 0", i, created)
		}
	}

	after, _ := repo.ListOccurrences(ctx, s.ID)
	if len(after) != len(before) {
		t.Fatalf("occurrence count changed from %d to %d across reruns", len(before), len(after))
	}
	for i := range after {
		if after[i].ID != before[i].ID || after[i].SequenceNo != before[i].SequenceNo {
			t.Errorf("occurrence %d changed across reruns", i)
		}
	}
}

// Time passing tops the horizon back up, and sequence numbers continue rather than
// restart -- a reused sequence number is a reused idempotency key.
func TestRunTopsUpAsTimeAdvances(t *testing.T) {
	db, repo := setup(t)
	ctx := context.Background()

	s := newSchedule(t, db, repo, domain.NewDate(2026, time.January, 1), 30)
	m := materialize.New(repo, 3, nil)

	if _, _, err := m.Run(ctx, s, domain.NewDate(2026, time.January, 15)); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Move past the first two occurrences (Jan 31 and Mar 2).
	created, _, err := m.Run(ctx, s, domain.NewDate(2026, time.March, 10))
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if created == 0 {
		t.Error("expected the horizon to be topped up after time advanced")
	}

	got, _ := repo.ListOccurrences(ctx, s.ID)
	seen := map[int]bool{}
	for _, o := range got {
		if seen[o.SequenceNo] {
			t.Fatalf("sequence number %d reused — that is a reused idempotency key", o.SequenceNo)
		}
		seen[o.SequenceNo] = true
	}
	if n, _ := repo.CountFutureplannedOccurrences(ctx, s.ID, domain.NewDate(2026, time.March, 10)); n != 3 {
		t.Errorf("future planned = %d, want the horizon of 3", n)
	}
}

// A schedule anchored long ago must not materialize the past.
func TestRunDoesNotMaterializeThePast(t *testing.T) {
	db, repo := setup(t)
	ctx := context.Background()

	s := newSchedule(t, db, repo, domain.NewDate(2020, time.January, 1), 30)
	today := domain.NewDate(2026, time.June, 15)
	m := materialize.New(repo, 3, nil)

	if _, _, err := m.Run(ctx, s, today); err != nil {
		t.Fatalf("run: %v", err)
	}

	got, _ := repo.ListOccurrences(ctx, s.ID)
	if len(got) != 3 {
		t.Fatalf("got %d occurrences, want 3", len(got))
	}
	for _, o := range got {
		if !o.ScheduledFor.After(today) {
			t.Errorf("materialized a past date: %s", o.ScheduledFor)
		}
	}
}

func TestRunSkipsInactiveSchedules(t *testing.T) {
	db, repo := setup(t)
	ctx := context.Background()

	s := newSchedule(t, db, repo, domain.NewDate(2026, time.January, 1), 30)
	for _, status := range []domain.ScheduleStatus{
		domain.SchedulePaused, domain.ScheduleCanceled, domain.ScheduleFailed,
	} {
		s.Status = status
		created, _, err := materialize.New(repo, 3, nil).Run(ctx, s, domain.NewDate(2026, time.January, 15))
		if err != nil {
			t.Fatalf("%s: %v", status, err)
		}
		if created != 0 {
			t.Errorf("%s schedule materialized %d occurrences, want 0", status, created)
		}
	}
}

// next_run_date is materialized for the indexed query in spec §3, and must be the
// earliest future planned occurrence.
func TestRunSetsNextRunDate(t *testing.T) {
	db, repo := setup(t)
	ctx := context.Background()

	anchor := domain.NewDate(2026, time.January, 1)
	s := newSchedule(t, db, repo, anchor, 30)
	today := domain.NewDate(2026, time.January, 15)

	if _, _, err := materialize.New(repo, 3, nil).Run(ctx, s, today); err != nil {
		t.Fatalf("run: %v", err)
	}

	got, err := repo.GetSchedule(ctx, s.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	want, _ := domain.OccurrenceDate(anchor, 30, 1) // 2026-01-31
	if got.NextRunDate == nil || !got.NextRunDate.Equal(want) {
		t.Fatalf("next_run_date = %v, want %s", got.NextRunDate, want)
	}
}

func TestRunAllProcessesEveryActiveSchedule(t *testing.T) {
	db, repo := setup(t)
	ctx := context.Background()

	a := newSchedule(t, db, repo, domain.NewDate(2026, time.January, 1), 30)
	b := newSchedule(t, db, repo, domain.NewDate(2026, time.February, 1), 60)

	res, err := materialize.New(repo, 2, nil).RunAll(ctx, domain.NewDate(2026, time.January, 15))
	if err != nil {
		t.Fatalf("run all: %v", err)
	}
	if res.OccurrencesCreated < 4 {
		t.Errorf("created %d occurrences across schedules, want at least 4", res.OccurrencesCreated)
	}
	for _, s := range []domain.Schedule{a, b} {
		n, _ := repo.CountFutureplannedOccurrences(ctx, s.ID, domain.NewDate(2026, time.January, 15))
		if n != 2 {
			t.Errorf("schedule %s has %d future planned, want 2", s.ID, n)
		}
	}
}

// The materializer writes to the append-only log so the spec §8 read models have
// something to project from.
func TestRunAppendsEvents(t *testing.T) {
	db, repo := setup(t)
	ctx := context.Background()

	s := newSchedule(t, db, repo, domain.NewDate(2026, time.January, 1), 30)
	if _, _, err := materialize.New(repo, 3, nil).Run(ctx, s, domain.NewDate(2026, time.January, 15)); err != nil {
		t.Fatalf("run: %v", err)
	}

	events, err := repo.ListEvents(ctx, s.ID)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want one per created occurrence (3)", len(events))
	}
	for _, e := range events {
		if e.Actor != domain.ActorSystem {
			t.Errorf("actor = %s, want system", e.Actor)
		}
		if e.EventType != domain.EventOccurrenceMaterialed {
			t.Errorf("event type = %s", e.EventType)
		}
	}
}

// Materializing across a DST transition must not shift a date. This is the whole
// reason cadence math lives in date space.
func TestRunAcrossDSTBoundary(t *testing.T) {
	db, repo := setup(t)
	ctx := context.Background()

	// Anchored a week before US spring-forward (2026-03-08), weekly cadence, so the
	// horizon spans the transition.
	anchor := domain.NewDate(2026, time.March, 1)
	s := newSchedule(t, db, repo, anchor, 7)

	if _, _, err := materialize.New(repo, 3, nil).Run(ctx, s, domain.NewDate(2026, time.March, 2)); err != nil {
		t.Fatalf("run: %v", err)
	}

	got, _ := repo.ListOccurrences(ctx, s.ID)
	for _, o := range got {
		want, _ := domain.OccurrenceDate(anchor, 7, o.SequenceNo)
		if !o.ScheduledFor.Equal(want) {
			t.Errorf("occurrence %d scheduled for %s, want %s — DST shifted the date",
				o.SequenceNo, o.ScheduledFor, want)
		}
	}
}

// Re-anchoring must not push the next order out by the schedule's whole history.
//
// This is the regression test for conflating two different numbers. sequence_no is a
// monotonic counter that never restarts, because a reused number is a reused
// idempotency key and therefore a duplicate charge. The cadence index is the n in
// anchor + (n × interval_days). They coincide only until something re-anchors the
// schedule, and spec §6 re-anchors on both resume and change_cadence.
//
// Before the fix, a schedule with three occurrences behind it that re-anchored to
// June 1 planned its next order at anchor + (4 × 30) = September 29 — four intervals
// out — instead of July 1. The customer's first order back would have been three
// months late, and every later one wrong by the same drift.
func TestRunAfterReAnchorPlansOneIntervalOut(t *testing.T) {
	db, repo := setup(t)
	ctx := context.Background()

	s := newSchedule(t, db, repo, domain.NewDate(2026, time.January, 1), 30)
	m := materialize.New(repo, 3, nil)

	if _, _, err := m.Run(ctx, s, domain.NewDate(2026, time.January, 15)); err != nil {
		t.Fatalf("first run: %v", err)
	}
	before, _ := repo.ListOccurrences(ctx, s.ID)
	if len(before) != 3 {
		t.Fatalf("setup: got %d occurrences, want 3", len(before))
	}

	// Simulate a resume: the old horizon is cleared and the schedule re-anchors.
	if _, err := repo.CancelUnexecutedOccurrences(ctx, s.ID); err != nil {
		t.Fatalf("clear horizon: %v", err)
	}
	newAnchor := domain.NewDate(2026, time.June, 1)
	if err := repo.UpdateScheduleCadence(ctx, s.ID, 30, newAnchor); err != nil {
		t.Fatalf("re-anchor: %v", err)
	}
	s.AnchorDate = newAnchor

	if _, _, err := m.Run(ctx, s, newAnchor); err != nil {
		t.Fatalf("run after re-anchor: %v", err)
	}

	planned, err := repo.ListPlannedOccurrences(ctx, s.ID)
	if err != nil {
		t.Fatalf("list planned: %v", err)
	}
	if len(planned) != 3 {
		t.Fatalf("got %d planned occurrences after re-anchor, want 3", len(planned))
	}

	// Dates come from the new anchor: July 1, July 31, August 30.
	for i, o := range planned {
		want := newAnchor.AddDays(30 * (i + 1))
		if !o.ScheduledFor.Equal(want) {
			t.Errorf("occurrence %d scheduled for %s, want %s — the cadence index was "+
				"taken from sequence_no instead of the new anchor", i+1, o.ScheduledFor, want)
		}
	}

	// Sequence numbers continue past the history rather than restarting, so no
	// idempotency key is ever reused.
	all, _ := repo.ListOccurrences(ctx, s.ID)
	seen := map[int]bool{}
	keys := map[string]bool{}
	for _, o := range all {
		if seen[o.SequenceNo] {
			t.Fatalf("sequence number %d reused — that is a reused idempotency key", o.SequenceNo)
		}
		seen[o.SequenceNo] = true
		if keys[o.IdempotencyKey] {
			t.Fatalf("idempotency key %q reused", o.IdempotencyKey)
		}
		keys[o.IdempotencyKey] = true
	}
	for _, o := range planned {
		if o.SequenceNo <= 3 {
			t.Errorf("post-re-anchor occurrence reused sequence number %d from before it", o.SequenceNo)
		}
	}
}

// A shorter cadence must take effect from the new anchor, not from wherever the old
// cadence had reached.
func TestRunAfterCadenceChangeUsesNewInterval(t *testing.T) {
	db, repo := setup(t)
	ctx := context.Background()

	s := newSchedule(t, db, repo, domain.NewDate(2026, time.January, 1), 90)
	m := materialize.New(repo, 2, nil)
	if _, _, err := m.Run(ctx, s, domain.NewDate(2026, time.January, 15)); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if _, err := repo.CancelUnexecutedOccurrences(ctx, s.ID); err != nil {
		t.Fatalf("clear horizon: %v", err)
	}

	anchor := domain.NewDate(2026, time.February, 1)
	if err := repo.UpdateScheduleCadence(ctx, s.ID, 14, anchor); err != nil {
		t.Fatalf("change cadence: %v", err)
	}
	s.AnchorDate, s.IntervalDays = anchor, 14

	if _, _, err := m.Run(ctx, s, anchor); err != nil {
		t.Fatalf("run: %v", err)
	}
	planned, _ := repo.ListPlannedOccurrences(ctx, s.ID)
	if len(planned) != 2 {
		t.Fatalf("got %d planned, want 2", len(planned))
	}
	for i, o := range planned {
		want := anchor.AddDays(14 * (i + 1))
		if !o.ScheduledFor.Equal(want) {
			t.Errorf("occurrence %d at %s, want %s", i+1, o.ScheduledFor, want)
		}
	}
}
