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
		return 0, 0, nil
	}

	// Continue from the highest sequence number already used, so a schedule that has
	// executed occurrences 1-5 plans 6 next. Sequence numbers are never reused: the
	// idempotency key derives from them, and a reused key is a duplicate charge.
	maxSeq, err := m.repo.MaxSequenceNo(ctx, s.ID)
	if err != nil {
		return 0, 0, err
	}

	seq := maxSeq + 1
	if maxSeq == 0 {
		// Nothing planned yet: start at the first occurrence falling after today, so
		// a schedule created with an old anchor does not materialize the past.
		firstSeq, _, err := domain.NextOccurrenceAfter(s.AnchorDate, s.IntervalDays, today)
		if err != nil {
			return 0, 0, err
		}
		seq = firstSeq
	}

	var earliest *domain.Date
	for i := 0; i < missing; i++ {
		date, err := domain.OccurrenceDate(s.AnchorDate, s.IntervalDays, seq)
		if err != nil {
			return created, duplicates, err
		}

		// Skip forward past any date already behind us, which can happen on a
		// schedule that was paused for longer than one interval.
		for !date.After(today) {
			seq++
			date, err = domain.OccurrenceDate(s.AnchorDate, s.IntervalDays, seq)
			if err != nil {
				return created, duplicates, err
			}
		}

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
		seq++
	}

	// next_run_date is materialized for the indexed query in spec §3. It is derived
	// from the anchor like every other date, never accumulated.
	if err := m.refreshNextRunDate(ctx, s, today); err != nil {
		return created, duplicates, err
	}
	return created, duplicates, nil
}

// refreshNextRunDate recomputes the schedule's next run from its planned occurrences.
func (m *Materializer) refreshNextRunDate(ctx context.Context, s domain.Schedule, today domain.Date) error {
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
