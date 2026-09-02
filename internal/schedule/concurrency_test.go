package schedule_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/EPW80/replenishment-system/internal/domain"
	"github.com/EPW80/replenishment-system/internal/store"
)

// These tests run real concurrent transactions against the database. They are the only
// way to hold the property that matters here: a transition is atomic against another
// transition. Every one of them passes trivially against a single-threaded
// implementation and fails against a read-then-write one, which is the point.
//
// The assertions are on counts and invariants rather than on timing, so the outcome is
// deterministic even though the interleaving is not — exactly one caller can win, no
// matter who gets there first.

// contend runs fn from n goroutines released together, and reports their errors.
//
// The barrier matters: goroutines started in a loop tend to run in sequence on a busy
// runner, which would let a broken implementation pass by never actually overlapping.
func contend(n int, fn func(i int) error) []error {
	var (
		start sync.WaitGroup
		done  sync.WaitGroup
	)
	start.Add(1)
	errs := make([]error, n)

	for i := 0; i < n; i++ {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			errs[i] = fn(i)
		}()
	}

	start.Done()
	done.Wait()
	return errs
}

// classify splits contention results into winners and precondition failures, failing
// the test on anything else — a deadlock or a driver error is not a valid outcome.
func classify(t *testing.T, errs []error) (won, conflicted int) {
	t.Helper()
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case domain.IsTransitionError(err):
			conflicted++
		default:
			t.Fatalf("unexpected error from a contending transition: %v", err)
		}
	}
	return won, conflicted
}

// Only one caller can pause a schedule. The rest must be told the precondition no
// longer holds, not silently applied on top of each other.
func TestConcurrentPausesLeaveExactlyOneWinner(t *testing.T) {
	repo, svc := setup(t)
	s := newActive(t, repo, domain.DateOf(fixedNow), 30)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const callers = 8
	won, conflicted := classify(t, contend(callers, func(int) error {
		_, err := svc.Pause(ctx, s.ID, nil, anyCaller)
		return err
	}))

	if won != 1 {
		t.Errorf("%d callers paused the same schedule, want exactly 1", won)
	}
	if conflicted != callers-1 {
		t.Errorf("got %d conflicts, want %d", conflicted, callers-1)
	}

	got, err := repo.GetSchedule(ctx, s.ID, store.SystemScope())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != domain.SchedulePaused {
		t.Errorf("status = %s, want paused", got.Status)
	}

	// The audit log is the reason this matters beyond the status field: two pause
	// events would describe a transition that only happened once.
	if n := countEvents(t, repo, s.ID, domain.EventSchedulePaused); n != 1 {
		t.Errorf("%d pause events recorded, want 1", n)
	}
}

// Cancel captures a churn reason, so a duplicate would double-count in the spec §8
// read models.
func TestConcurrentCancelsRecordOneReason(t *testing.T) {
	repo, svc := setup(t)
	s := newActive(t, repo, domain.DateOf(fixedNow), 30)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const callers = 8
	won, conflicted := classify(t, contend(callers, func(int) error {
		_, err := svc.Cancel(ctx, s.ID, "too_expensive", anyCaller)
		return err
	}))

	if won != 1 {
		t.Errorf("%d callers canceled the same schedule, want exactly 1", won)
	}
	if conflicted != callers-1 {
		t.Errorf("got %d conflicts, want %d", conflicted, callers-1)
	}
	if n := countEvents(t, repo, s.ID, domain.EventScheduleCanceled); n != 1 {
		t.Errorf("%d cancel events recorded, want 1", n)
	}
}

// Concurrent skips are not mutually exclusive — each one legitimately takes the next
// remaining order. What must never happen is two of them taking the *same* one, which
// is what a target chosen before the lock allows: the customer asks to skip one
// shipment and two disappear.
func TestConcurrentSkipsNeverTakeTheSameOccurrence(t *testing.T) {
	repo, svc := setup(t)
	s := newActive(t, repo, domain.DateOf(fixedNow), 30)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const callers = 3
	won, _ := classify(t, contend(callers, func(int) error {
		_, err := svc.SkipNext(ctx, s.ID, anyCaller)
		return err
	}))

	occurrences, err := repo.ListOccurrences(ctx, s.ID, store.SystemScope())
	if err != nil {
		t.Fatalf("list occurrences: %v", err)
	}

	skipped := 0
	seen := map[int]bool{}
	for _, o := range occurrences {
		if o.Status != domain.OccurrenceSkipped {
			continue
		}
		if seen[o.SequenceNo] {
			t.Errorf("sequence %d skipped twice", o.SequenceNo)
		}
		seen[o.SequenceNo] = true
		skipped++
	}

	// One skipped order per successful call: no more (two callers sharing a target),
	// and no fewer (a skip that reported success without moving anything).
	if skipped != won {
		t.Errorf("%d occurrences skipped for %d successful skips", skipped, won)
	}
	if n := countEvents(t, repo, s.ID, domain.EventOccurrenceSkipped); n != won {
		t.Errorf("%d skip events for %d successful skips", n, won)
	}
}

// A concurrent pause and resume must land in a state some serial order could have
// produced, rather than in one only interleaving can explain.
//
// Both orders are legitimate, which is what makes this worth asserting rather than
// picking a winner: resume-then-pause leaves the schedule paused with the resume
// refused, and pause-then-resume leaves it active with both succeeding. What must
// never happen is a result that matches neither — a resume that succeeded against a
// schedule the pause then paused, or a status disagreeing with the outcomes the
// callers were handed.
func TestPauseAndResumeSerialize(t *testing.T) {
	repo, svc := setup(t)
	s := newActive(t, repo, domain.DateOf(fixedNow), 30)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	errs := contend(2, func(i int) error {
		if i == 0 {
			_, err := svc.Pause(ctx, s.ID, nil, anyCaller)
			return err
		}
		_, err := svc.Resume(ctx, s.ID, anyCaller)
		return err
	})
	pauseErr, resumeErr := errs[0], errs[1]

	// Pause succeeds either way: the schedule is active to begin with, and a resume
	// that ran first was refused without changing anything.
	if pauseErr != nil {
		t.Fatalf("pause failed: %v", pauseErr)
	}
	if resumeErr != nil && !domain.IsTransitionError(resumeErr) {
		t.Fatalf("resume error = %v, want nil or a precondition failure", resumeErr)
	}

	got, err := repo.GetSchedule(ctx, s.ID, store.SystemScope())
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	// The status has to agree with what the callers were told.
	want := domain.SchedulePaused
	if resumeErr == nil {
		want = domain.ScheduleActive // resume ran after the pause and undid it
	}
	if got.Status != want {
		t.Errorf("status = %s, want %s (resume err = %v)", got.Status, want, resumeErr)
	}

	// Whichever order ran, the pause happened exactly once.
	if n := countEvents(t, repo, s.ID, domain.EventSchedulePaused); n != 1 {
		t.Errorf("%d pause events recorded, want 1", n)
	}
}

func countEvents(t *testing.T, repo *store.PostgresRepository, scheduleID, eventType string) int {
	t.Helper()
	events, err := repo.ListEvents(context.Background(), scheduleID)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	n := 0
	for _, e := range events {
		if e.EventType == eventType {
			n++
		}
	}
	return n
}
