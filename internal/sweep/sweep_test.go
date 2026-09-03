package sweep_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EPW80/replenishment-system/internal/domain"
	"github.com/EPW80/replenishment-system/internal/materialize"
	"github.com/EPW80/replenishment-system/internal/schedule"
	"github.com/EPW80/replenishment-system/internal/store"
	"github.com/EPW80/replenishment-system/internal/sweep"
	"github.com/EPW80/replenishment-system/internal/testsupport"
)

// fixedClock returns a clock stopped at t, so a test can place a schedule in time
// rather than wait for one.
func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

// newRepo opens one private schema for the test. Every clock below is built against
// this same repository — calling testsupport.DB twice would hand back two empty
// databases and the sweep would find nothing to do.
func newRepo(t *testing.T) *store.PostgresRepository {
	t.Helper()
	return store.New(testsupport.DB(t))
}

// at wires a Sweeper and the Service it drives onto one clock stopped at now, the way
// cmd/sweep does. Sharing the clock is the point: the two disagree about what day it
// is on the boundary otherwise.
//
// Advancing time means calling this again with a later instant against the same repo.
func at(repo *store.PostgresRepository, now time.Time) (*schedule.Service, *sweep.Sweeper) {
	clock := fixedClock(now)
	svc := schedule.New(repo, materialize.New(repo, 3, nil), clock)
	return svc, sweep.New(repo, svc, clock, nil)
}

func newActiveSchedule(t *testing.T, repo *store.PostgresRepository, tz string) domain.Schedule {
	t.Helper()
	s := domain.Schedule{
		ID:            uuid.NewString(),
		CustomerID:    "cust_" + uuid.NewString()[:8],
		OriginOrderID: "order_" + uuid.NewString(),
		Status:        domain.ScheduleActive,
		IntervalDays:  30,
		AnchorDate:    domain.NewDate(2026, time.January, 1),
		Timezone:      tz,
	}
	if err := repo.CreateSchedule(context.Background(), s, []domain.ScheduleItem{{
		ID: uuid.NewString(), ScheduleID: s.ID, SKU: "SKU-A", Quantity: 1,
	}}); err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	return s
}

func customer(id string) schedule.Caller {
	return schedule.Caller{Actor: domain.ActorCustomer, Scope: store.CustomerScope(id)}
}

func mustPause(t *testing.T, svc *schedule.Service, s domain.Schedule, until *domain.Date) {
	t.Helper()
	if _, err := svc.Pause(context.Background(), s.ID, until, customer(s.CustomerID)); err != nil {
		t.Fatalf("pause: %v", err)
	}
}

func reload(t *testing.T, repo *store.PostgresRepository, id string) domain.Schedule {
	t.Helper()
	s, err := repo.GetSchedule(context.Background(), id, store.SystemScope())
	if err != nil {
		t.Fatalf("reload schedule: %v", err)
	}
	return s
}

// The bug this package exists to fix: a pause with an end date used to run forever,
// because nothing ever read paused_until.
func TestResumeDueEndsATimedPause(t *testing.T) {
	repo := newRepo(t)
	svc, _ := at(repo, time.Date(2026, time.March, 1, 12, 0, 0, 0, time.UTC))

	s := newActiveSchedule(t, repo, "America/Los_Angeles")
	until := domain.NewDate(2026, time.March, 10)
	mustPause(t, svc, s, &until)

	if got := reload(t, repo, s.ID); got.Status != domain.SchedulePaused {
		t.Fatalf("precondition: status = %s, want paused", got.Status)
	}

	// The day arrives.
	_, swp := at(repo, time.Date(2026, time.March, 10, 12, 0, 0, 0, time.UTC))

	res, err := swp.ResumeDue(context.Background())
	if err != nil {
		t.Fatalf("resume due: %v", err)
	}
	if res.Resumed != 1 {
		t.Fatalf("resumed = %d, want 1 (result: %+v)", res.Resumed, res)
	}

	got := reload(t, repo, s.ID)
	if got.Status != domain.ScheduleActive {
		t.Errorf("status = %s, want active", got.Status)
	}
	if got.PausedUntil != nil {
		t.Errorf("paused_until = %v, want cleared", got.PausedUntil)
	}
	// Resume re-anchors to today (spec §6), so the customer is not charged for the
	// shipments the pause skipped.
	if want := domain.NewDate(2026, time.March, 10); !got.AnchorDate.Equal(want) {
		t.Errorf("anchor_date = %s, want %s", got.AnchorDate, want)
	}
	if got.NextRunDate == nil {
		t.Error("next_run_date is nil, want the horizon re-materialized")
	}
}

// An indefinite pause is a different feature and must survive the sweep untouched.
// Resuming one would start charging a customer who never named a date.
func TestResumeDueLeavesAnIndefinitePauseAlone(t *testing.T) {
	repo := newRepo(t)
	svc, _ := at(repo, time.Date(2026, time.March, 1, 12, 0, 0, 0, time.UTC))

	s := newActiveSchedule(t, repo, "America/Los_Angeles")
	mustPause(t, svc, s, nil)

	// A year later, still paused.
	_, swp := at(repo, time.Date(2027, time.March, 1, 12, 0, 0, 0, time.UTC))
	res, err := swp.ResumeDue(context.Background())
	if err != nil {
		t.Fatalf("resume due: %v", err)
	}
	if res.Resumed != 0 {
		t.Errorf("resumed = %d, want 0", res.Resumed)
	}
	if got := reload(t, repo, s.ID); got.Status != domain.SchedulePaused {
		t.Errorf("status = %s, want paused", got.Status)
	}
}

// Pause requires the date to be strictly in the future, so the day named is the first
// day the schedule may come back: paused_until == today resumes.
func TestResumeDueIsInclusiveOfTheNamedDay(t *testing.T) {
	repo := newRepo(t)
	svc, _ := at(repo, time.Date(2026, time.March, 1, 12, 0, 0, 0, time.UTC))

	s := newActiveSchedule(t, repo, "UTC")
	until := domain.NewDate(2026, time.March, 10)
	mustPause(t, svc, s, &until)

	_, onTheDay := at(repo, time.Date(2026, time.March, 10, 0, 30, 0, 0, time.UTC))
	res, err := onTheDay.ResumeDue(context.Background())
	if err != nil {
		t.Fatalf("resume due: %v", err)
	}
	if res.Resumed != 1 {
		t.Fatalf("resumed = %d, want 1 (result: %+v)", res.Resumed, res)
	}
}

// The day before is not the day. A schedule resumed early starts charging sooner than
// the customer agreed to.
func TestResumeDueLeavesAPauseThatHasNotComeDue(t *testing.T) {
	repo := newRepo(t)
	svc, _ := at(repo, time.Date(2026, time.March, 1, 12, 0, 0, 0, time.UTC))

	s := newActiveSchedule(t, repo, "UTC")
	until := domain.NewDate(2026, time.March, 10)
	mustPause(t, svc, s, &until)

	_, dayBefore := at(repo, time.Date(2026, time.March, 9, 23, 0, 0, 0, time.UTC))
	res, err := dayBefore.ResumeDue(context.Background())
	if err != nil {
		t.Fatalf("resume due: %v", err)
	}
	if res.Resumed != 0 {
		t.Errorf("resumed = %d, want 0", res.Resumed)
	}
	if got := reload(t, repo, s.ID); got.Status != domain.SchedulePaused {
		t.Errorf("status = %s, want paused", got.Status)
	}
}

// "Due" is a question about the customer's calendar, not the server's. A schedule in a
// timezone behind UTC is not yet on its resume day when UTC has already turned over,
// and the query over-selects by a day precisely so this case is checked rather than
// missed.
func TestResumeDueRespectsTheCustomerTimezone(t *testing.T) {
	repo := newRepo(t)
	svc, _ := at(repo, time.Date(2026, time.March, 1, 12, 0, 0, 0, time.UTC))

	// UTC-10. At 2026-03-10T05:00Z it is still 2026-03-09 in Honolulu.
	s := newActiveSchedule(t, repo, "Pacific/Honolulu")
	until := domain.NewDate(2026, time.March, 10)
	mustPause(t, svc, s, &until)

	_, swp := at(repo, time.Date(2026, time.March, 10, 5, 0, 0, 0, time.UTC))
	res, err := swp.ResumeDue(context.Background())
	if err != nil {
		t.Fatalf("resume due: %v", err)
	}
	if res.Resumed != 0 {
		t.Errorf("resumed = %d, want 0 — still 2026-03-09 in Honolulu", res.Resumed)
	}
	if res.NotYetDue != 1 {
		t.Errorf("not_yet_due = %d, want 1 (result: %+v)", res.NotYetDue, res)
	}

	// Later the same UTC day, Honolulu has turned over.
	_, later := at(repo, time.Date(2026, time.March, 10, 20, 0, 0, 0, time.UTC))
	res, err = later.ResumeDue(context.Background())
	if err != nil {
		t.Fatalf("resume due: %v", err)
	}
	if res.Resumed != 1 {
		t.Errorf("resumed = %d, want 1 (result: %+v)", res.Resumed, res)
	}
}

// A schedule in a timezone ahead of UTC reaches its resume day first. The query's
// extra day is what keeps it from waiting an entire run cycle.
func TestResumeDueCatchesATimezoneAheadOfUTC(t *testing.T) {
	repo := newRepo(t)
	svc, _ := at(repo, time.Date(2026, time.March, 1, 12, 0, 0, 0, time.UTC))

	// UTC+13. At 2026-03-09T22:00Z it is already 2026-03-10 in Auckland.
	s := newActiveSchedule(t, repo, "Pacific/Auckland")
	until := domain.NewDate(2026, time.March, 10)
	mustPause(t, svc, s, &until)

	_, swp := at(repo, time.Date(2026, time.March, 9, 22, 0, 0, 0, time.UTC))
	res, err := swp.ResumeDue(context.Background())
	if err != nil {
		t.Fatalf("resume due: %v", err)
	}
	if res.Resumed != 1 {
		t.Errorf("resumed = %d, want 1 — already 2026-03-10 in Auckland (result: %+v)", res.Resumed, res)
	}
}

// The job runs nightly and will see the same schedule again. A second pass must be a
// no-op rather than a second resume with a second audit event.
func TestResumeDueIsIdempotent(t *testing.T) {
	repo := newRepo(t)
	svc, _ := at(repo, time.Date(2026, time.March, 1, 12, 0, 0, 0, time.UTC))

	s := newActiveSchedule(t, repo, "UTC")
	until := domain.NewDate(2026, time.March, 10)
	mustPause(t, svc, s, &until)

	_, swp := at(repo, time.Date(2026, time.March, 10, 12, 0, 0, 0, time.UTC))

	if _, err := swp.ResumeDue(context.Background()); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	res, err := swp.ResumeDue(context.Background())
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if res.Considered != 0 {
		t.Errorf("considered = %d, want 0 — paused_until is cleared on resume", res.Considered)
	}

	if n := countEvents(t, repo, s.ID, domain.EventScheduleResumed); n != 1 {
		t.Errorf("resume events = %d, want exactly 1", n)
	}
}

// The event log records what actually happened. Nobody signed in to cause this, so it
// must not claim a customer did.
func TestResumeDueRecordsTheSystemActor(t *testing.T) {
	repo := newRepo(t)
	svc, _ := at(repo, time.Date(2026, time.March, 1, 12, 0, 0, 0, time.UTC))

	s := newActiveSchedule(t, repo, "UTC")
	until := domain.NewDate(2026, time.March, 10)
	mustPause(t, svc, s, &until)

	_, swp := at(repo, time.Date(2026, time.March, 10, 12, 0, 0, 0, time.UTC))
	if _, err := swp.ResumeDue(context.Background()); err != nil {
		t.Fatalf("resume due: %v", err)
	}

	events, err := repo.ListEvents(context.Background(), s.ID)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var found bool
	for _, e := range events {
		if e.EventType != domain.EventScheduleResumed {
			continue
		}
		found = true
		if e.Actor != domain.ActorSystem {
			t.Errorf("actor = %s, want %s", e.Actor, domain.ActorSystem)
		}
	}
	if !found {
		t.Fatal("no schedule.resumed event recorded")
	}
}

// A canceled schedule has no paused_until and must never be swept back to life.
func TestResumeDueIgnoresCanceledSchedules(t *testing.T) {
	repo := newRepo(t)
	svc, _ := at(repo, time.Date(2026, time.March, 1, 12, 0, 0, 0, time.UTC))

	s := newActiveSchedule(t, repo, "UTC")
	until := domain.NewDate(2026, time.March, 10)
	mustPause(t, svc, s, &until)

	if _, err := svc.Cancel(context.Background(), s.ID, domain.ReasonTooExpensive, customer(s.CustomerID)); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	_, swp := at(repo, time.Date(2026, time.March, 10, 12, 0, 0, 0, time.UTC))
	res, err := swp.ResumeDue(context.Background())
	if err != nil {
		t.Fatalf("resume due: %v", err)
	}
	if res.Considered != 0 || res.Resumed != 0 {
		t.Errorf("result = %+v, want nothing considered or resumed", res)
	}
	if got := reload(t, repo, s.ID); got.Status != domain.ScheduleCanceled {
		t.Errorf("status = %s, want canceled", got.Status)
	}
}

func countEvents(t *testing.T, repo *store.PostgresRepository, scheduleID string, want string) int {
	t.Helper()
	events, err := repo.ListEvents(context.Background(), scheduleID)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var n int
	for _, e := range events {
		if e.EventType == want {
			n++
		}
	}
	return n
}
