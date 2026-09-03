// Package sweep runs the periodic passes that move schedules forward on their own,
// without a customer or an operator asking.
//
// Every transition here still goes through internal/schedule, so a schedule the sweep
// touches is locked, validated and audited exactly as it would be for a customer.
// Only the actor recorded on the event differs.
//
// It reasons about dates and nothing else. A sweep decides when the next *order* is
// placed; it never records, infers or reports anything about the product being used
// (spec §2).
//
// Spec §5 step 4 Reconcile lands here too, once there is an execution path to
// reconcile against.
package sweep

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/EPW80/replenishment-system/internal/domain"
	"github.com/EPW80/replenishment-system/internal/schedule"
	"github.com/EPW80/replenishment-system/internal/store"
)

// Sweeper runs the periodic passes.
type Sweeper struct {
	repo store.Repository
	svc  *schedule.Service
	log  *slog.Logger

	// now is injected so tests can place a schedule in time rather than sleep. It
	// must be the same clock the Service was built with, or the two disagree about
	// what day it is on the boundary this job exists to get right.
	now func() time.Time
}

// New returns a Sweeper. now nil uses time.Now; log nil uses the default logger.
func New(repo store.Repository, svc *schedule.Service, now func() time.Time, log *slog.Logger) *Sweeper {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.Default()
	}
	return &Sweeper{repo: repo, svc: svc, log: log, now: now}
}

// ResumeResult reports what one resume pass did.
type ResumeResult struct {
	Considered int
	Resumed    int

	// NotYetDue counts schedules whose paused_until has arrived in UTC but not yet in
	// the customer's own timezone. They are picked up on the next run.
	NotYetDue int

	// AlreadyMoved counts schedules a customer changed between the listing and the
	// lock. Not a failure: the customer's action wins.
	AlreadyMoved int
}

// ResumeDue ends every timed pause that has come due.
//
// A pause with no paused_until is indefinite and is never touched — the customer said
// "until I say otherwise," and a sweep that resumed them anyway would start charging
// someone who never asked to restart.
//
// One schedule's failure does not abort the run, matching the materializer: a single
// bad schedule must not strand every other customer in a pause they set an end date
// on. Failures are logged and the run continues, returning the first error once every
// candidate has been attempted.
func (s *Sweeper) ResumeDue(ctx context.Context) (ResumeResult, error) {
	runDate := domain.DateOf(s.now().UTC())

	candidates, err := s.repo.ListSchedulesDueToResume(ctx, runDate)
	if err != nil {
		return ResumeResult{}, fmt.Errorf("list schedules due to resume: %w", err)
	}

	var res ResumeResult
	var firstErr error

	for _, sched := range candidates {
		res.Considered++

		// The query deliberately over-selects by a day; this is where the customer's
		// own calendar decides. Pause required the date to be after their today, so
		// resuming asks the mirror question against the same clock.
		if sched.PausedUntil.After(s.todayFor(sched)) {
			res.NotYetDue++
			continue
		}

		// Resume locks the row and re-checks the precondition, so a schedule the
		// customer resumed or canceled since the listing fails here rather than being
		// written over. That is the intended outcome, not an error to report.
		if _, err := s.svc.Resume(ctx, sched.ID, systemCaller()); err != nil {
			if domain.IsTransitionError(err) || errors.Is(err, store.ErrNotFound) {
				res.AlreadyMoved++
				continue
			}
			s.log.Error("resume schedule failed", "schedule_id", sched.ID, "error", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}

		res.Resumed++
		s.log.Info("resumed schedule", "schedule_id", sched.ID,
			"paused_until", sched.PausedUntil.String())
	}

	return res, firstErr
}

// todayFor returns the current calendar date in the schedule's own timezone.
//
// Same fallback as internal/schedule: an unparseable timezone resolves to UTC rather
// than failing, because leaving a customer paused forever over a malformed timezone
// string is the worse outcome and is the very bug this job fixes.
func (s *Sweeper) todayFor(sched domain.Schedule) domain.Date {
	loc, err := time.LoadLocation(sched.Timezone)
	if err != nil {
		s.log.Warn("schedule has an unparseable timezone, treating as UTC",
			"schedule_id", sched.ID, "timezone", sched.Timezone)
		loc = time.UTC
	}
	return domain.DateOf(s.now().In(loc))
}

// systemCaller is the identity the sweep acts under.
//
// ActorSystem rather than ActorCustomer: the event log records what actually happened,
// and nobody signed in to cause this. SystemScope because there is no customer session
// to scope to — the job runs across every customer by design.
func systemCaller() schedule.Caller {
	return schedule.Caller{Actor: domain.ActorSystem, Scope: store.SystemScope()}
}
