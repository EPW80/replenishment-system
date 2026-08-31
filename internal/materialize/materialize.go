// Package materialize maintains the rolling horizon of planned occurrences.
//
// Spec §5 step 1: "for each active schedule, ensure 3 future planned occurrences
// exist." Materializing ahead of time is what lets the customer portal show a real
// upcoming queue, and what gives skip and defer something concrete to act on.
//
// Steps 2-4 of spec §5 (Arm at T-72h, Execute at T-0, Reconcile nightly) are Phase 2
// and later: they place orders and move money, and Phase 1 places no orders.
package materialize

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/EPW80/replenishment-system/internal/domain"
	"github.com/EPW80/replenishment-system/internal/store"
)

// Queue is the durable job queue this package enqueues work onto.
//
// Phase 1 runs the materializer synchronously and needs no queue, but the interface
// is defined here so that wiring river in (docs/adr/0002) is an adapter change rather
// than a rewrite. Spec §12 treats anything else as a design bug.
type Queue interface {
	Enqueue(ctx context.Context, kind string, payload []byte) error
}

// Materializer keeps each active schedule's planned-occurrence horizon topped up.
type Materializer struct {
	repo    store.Repository
	horizon int
	log     *slog.Logger

	// newID is injected so tests can produce deterministic identifiers.
	newID func() string
}

// New returns a Materializer maintaining the given horizon.
func New(repo store.Repository, horizon int, log *slog.Logger) *Materializer {
	if horizon < 1 {
		horizon = 1
	}
	if log == nil {
		log = slog.Default()
	}
	return &Materializer{repo: repo, horizon: horizon, log: log, newID: func() string { return uuid.NewString() }}
}

// WithRepo returns a copy of the materializer bound to repo.
//
// A spec §6 transition re-materializes as part of its own transaction, so it needs a
// materializer speaking to that transaction rather than to the pool. Copying keeps the
// horizon and logger while swapping only the data access.
func (m *Materializer) WithRepo(repo store.Repository) *Materializer {
	c := *m
	c.repo = repo
	return &c
}

// Result reports what one run did.
type Result struct {
	SchedulesConsidered int
	OccurrencesCreated  int
	DuplicatesSkipped   int
}

// RunAll tops up the horizon for every active schedule.
//
// One schedule's failure does not abort the run: a single bad schedule must not stop
// every other customer's orders from being planned. Failures are logged and the run
// continues, returning the first error once every schedule has been attempted.
func (m *Materializer) RunAll(ctx context.Context, today domain.Date) (Result, error) {
	schedules, err := m.repo.ListActiveSchedules(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("list active schedules: %w", err)
	}

	var res Result
	var firstErr error
	for _, s := range schedules {
		res.SchedulesConsidered++
		created, dupes, err := m.Run(ctx, s, today)
		res.OccurrencesCreated += created
		res.DuplicatesSkipped += dupes
		if err != nil {
			m.log.Error("materialize schedule failed", "schedule_id", s.ID, "error", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return res, firstErr
}

// Run tops up the horizon for one schedule.
//
// It is idempotent: running it twice in a row creates nothing the second time. That
// matters because the nightly job, a retry, and a manual invocation can all overlap,
// and because a duplicate occurrence would carry a duplicate idempotency key into
// order creation.
func (m *Materializer) Run(ctx context.Context, s domain.Schedule, today domain.Date) (created, duplicates int, err error) {
	if !s.IsActive() {
		return 0, 0, nil
	}
	if err := domain.ValidateInterval(s.IntervalDays); err != nil {
		return 0, 0, fmt.Errorf("schedule %s: %w", s.ID, err)
	}

	existing, err := m.repo.CountFutureplannedOccurrences(ctx, s.ID, today)
	if err != nil {
		return 0, 0, err
	}
	missing := m.horizon - existing
	if missing <= 0 {
		// The horizon is full, but the *earliest* date may still have moved — a skip
		// or a defer changes which occurrence is next without changing how many are
		// planned. Refresh before returning rather than leaving next_run_date stale.
		return 0, 0, m.RefreshNextRunDate(ctx, s, today)
	}

	// sequence_no and the cadence index are two different numbers and must not be
	// conflated.
	//
	// sequence_no is a monotonic per-schedule counter whose only job is to make the
	// idempotency key unique and stable: it continues from the highest already used,
	// so a schedule that has executed 1-5 plans 6 next, and a number is never reused
	// because a reused key is a duplicate charge.
	//
	// The cadence index is the n in anchor + (n × interval_days). It is derived from
	// the schedule's *current* anchor every time, never stored and never assumed equal
	// to sequence_no — resume and change_cadence both re-anchor (spec §6), and after
	// either one the two numbers diverge permanently.
	maxSeq, err := m.repo.MaxSequenceNo(ctx, s.ID)
	if err != nil {
		return 0, 0, err
	}
	seq := maxSeq

	// Continue the cadence from the furthest-out date already spoken for, or from
	// today when there is none. Starting at today is what stops a schedule with an old
	// anchor from materializing the past.
	cursor := today
	latest, err := m.repo.LatestScheduledDate(ctx, s.ID)
	if err != nil {
		return 0, 0, err
	}
	if latest != nil && latest.After(cursor) {
		cursor = *latest
	}

	var earliest *domain.Date
	for i := 0; i < missing; i++ {
		// Anchor-relative, every time: NextOccurrenceAfter returns anchor + (n ×
		// interval) for the first n landing strictly after the cursor. Advancing the
		// cursor to each date it returns walks the cadence forward without ever
		// accumulating one interval onto the last (spec §3, docs/adr/0004).
		_, date, err := domain.NextOccurrenceAfter(s.AnchorDate, s.IntervalDays, cursor)
		if err != nil {
			return created, duplicates, err
		}
		cursor = date
		seq++

		occ := domain.Occurrence{
			ID:             m.newID(),
			ScheduleID:     s.ID,
			SequenceNo:     seq,
			ScheduledFor:   date,
			Status:         domain.OccurrencePlanned,
			IdempotencyKey: domain.IdempotencyKey(s.ID, seq),
		}

		switch err := m.repo.CreateOccurrence(ctx, occ); {
		case errors.Is(err, store.ErrDuplicateOccurrence):
			// Already materialized by a concurrent run. That is the unique constraint
			// doing its job, not a failure — count it and move on.
			duplicates++
		case err != nil:
			return created, duplicates, err
		default:
			created++
			if earliest == nil || date.Before(*earliest) {
				d := date
				earliest = &d
			}
			if err := m.repo.AppendEvent(ctx, domain.ScheduleEvent{
				ScheduleID: s.ID,
				EventType:  domain.EventOccurrenceMaterialed,
				Actor:      domain.ActorSystem,
				Payload:    []byte(fmt.Sprintf(`{"sequence_no":%d,"scheduled_for":%q}`, seq, date.String())),
			}); err != nil {
				return created, duplicates, err
			}
		}
	}

	// next_run_date is materialized for the indexed query in spec §3. It is derived
	// from the anchor like every other date, never accumulated.
	if err := m.RefreshNextRunDate(ctx, s, today); err != nil {
		return created, duplicates, err
	}
	return created, duplicates, nil
}

// RefreshNextRunDate recomputes the schedule's next run from its planned occurrences.
//
// Exported because the spec §6 transitions need it on its own: skipping or deferring
// changes which occurrence is next without creating one, and next_run_date backs the
// indexed query in spec §3, so a stale value hides a schedule from the execution
// sweep.
func (m *Materializer) RefreshNextRunDate(ctx context.Context, s domain.Schedule, today domain.Date) error {
	planned, err := m.repo.ListPlannedOccurrences(ctx, s.ID)
	if err != nil {
		return err
	}

	var next *domain.Date
	for _, o := range planned {
		if !o.ScheduledFor.After(today) {
			continue
		}
		if next == nil || o.ScheduledFor.Before(*next) {
			d := o.ScheduledFor
			next = &d
		}
	}
	return m.repo.UpdateScheduleNextRun(ctx, s.ID, next)
}
