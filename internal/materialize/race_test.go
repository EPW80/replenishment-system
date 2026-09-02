package materialize_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/EPW80/replenishment-system/internal/domain"
	"github.com/EPW80/replenishment-system/internal/materialize"
	"github.com/EPW80/replenishment-system/internal/store"
)

// pausesAfterListing is a repository that pauses one schedule immediately after
// RunAll lists the active ones.
//
// That is the race, made deterministic. The nightly job walks every active schedule,
// which takes long enough for a customer to pause one in the middle of the run; the
// listing it is working from is a snapshot from before they did. Reproducing that with
// real goroutines would be timing-dependent and would pass by luck; interposing on the
// listing pins the interleaving exactly where the bug lives.
type pausesAfterListing struct {
	store.Repository

	t          *testing.T
	scheduleID string
	once       sync.Once
}

func (p *pausesAfterListing) ListActiveSchedules(ctx context.Context) ([]domain.Schedule, error) {
	listed, err := p.Repository.ListActiveSchedules(ctx)
	if err != nil {
		return nil, err
	}
	p.once.Do(func() {
		// Commits before RunAll acts on what it just read, exactly as a customer's
		// pause would.
		if err := p.Repository.UpdateScheduleStatus(
			ctx, p.scheduleID, domain.SchedulePaused, nil); err != nil {
			p.t.Fatalf("pause during listing: %v", err)
		}
	})
	return listed, nil
}

// A schedule paused after the listing must not have orders planned onto it.
//
// Planning from the snapshot leaves a paused schedule showing upcoming orders, which
// is the state Pause deliberately clears: a customer looking at it reasonably believes
// they are still going to be charged.
func TestRunAllDoesNotMaterializeASchedulePausedMidRun(t *testing.T) {
	db, repo := setup(t)
	ctx := context.Background()

	anchor := domain.NewDate(2026, time.January, 1)
	s := newSchedule(t, db, repo, anchor, 30)
	today := domain.NewDate(2026, time.January, 15)

	interfering := &pausesAfterListing{Repository: repo, t: t, scheduleID: s.ID}

	res, err := materialize.New(interfering, 3, nil).RunAll(ctx, today)
	if err != nil {
		t.Fatalf("run all: %v", err)
	}

	if res.OccurrencesCreated != 0 {
		t.Errorf("created %d occurrences on a schedule paused mid-run, want 0",
			res.OccurrencesCreated)
	}

	occurrences, err := repo.ListOccurrences(ctx, s.ID, store.SystemScope())
	if err != nil {
		t.Fatalf("list occurrences: %v", err)
	}
	for _, o := range occurrences {
		if o.Status == domain.OccurrencePlanned {
			t.Errorf("sequence %d is planned on a paused schedule", o.SequenceNo)
		}
	}

	// The pause must also survive the run: materializing must not write next_run_date
	// back onto a schedule that is no longer active.
	got, err := repo.GetSchedule(ctx, s.ID, store.SystemScope())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != domain.SchedulePaused {
		t.Errorf("status = %s, want paused", got.Status)
	}
	if got.NextRunDate != nil {
		t.Errorf("next_run_date = %s on a paused schedule, want none", got.NextRunDate)
	}
}

// An active schedule still gets its horizon through the same locked path, so the fix
// above is not simply refusing to materialize.
func TestRunAllStillMaterializesActiveSchedules(t *testing.T) {
	db, repo := setup(t)
	ctx := context.Background()

	s := newSchedule(t, db, repo, domain.NewDate(2026, time.January, 1), 30)
	today := domain.NewDate(2026, time.January, 15)

	res, err := materialize.New(repo, 3, nil).RunAll(ctx, today)
	if err != nil {
		t.Fatalf("run all: %v", err)
	}
	if res.OccurrencesCreated != 3 {
		t.Errorf("created %d occurrences, want 3", res.OccurrencesCreated)
	}

	got, err := repo.GetSchedule(ctx, s.ID, store.SystemScope())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.NextRunDate == nil {
		t.Error("next_run_date was not set on an active schedule")
	}
}
