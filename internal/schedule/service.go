// Package schedule applies the spec §6 state transitions.
//
// It is the only place a schedule's status changes. Each method loads the schedule,
// checks the transition against the pure rules in internal/domain, then applies the
// mutation and appends the audit event inside one transaction — so the event log and
// the state it describes can never disagree.
//
// Nothing here reasons about consumption. A transition changes when the next *order*
// is placed; it never records, infers, or reports anything about the product being
// used (spec §2).
package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/EPW80/replenishment-system/internal/domain"
	"github.com/EPW80/replenishment-system/internal/materialize"
	"github.com/EPW80/replenishment-system/internal/store"
)

// Caller is the verified identity a transition is performed on behalf of.
//
// Actor and Scope travel together because they answer the same question from two
// sides: who the event log should record, and which schedules this caller may touch at
// all. Passing them as one value means a new call site cannot supply the actor and
// silently forget the scope, which would read as an audit-trail detail while actually
// being an authorization hole.
//
// It carries no token and no header — internal/auth turns a credential into this, so
// the state machine never learns how callers are authenticated.
type Caller struct {
	Actor domain.EventActor
	Scope store.Scope
}

// Service applies transitions to schedules.
type Service struct {
	repo store.Repository
	mat  *materialize.Materializer
	now  func() time.Time
}

// New returns a Service. now is injected so tests are not clock-dependent; nil uses
// time.Now.
func New(repo store.Repository, mat *materialize.Materializer, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{repo: repo, mat: mat, now: now}
}

// today returns the current calendar date in the schedule's own timezone.
//
// The customer's timezone decides which day it is for them, and a schedule near the
// international date line is a day off if this is computed in UTC. An unparseable
// timezone falls back to UTC rather than failing the transition: refusing to let a
// customer cancel because their stored timezone is malformed would be the worse bug.
func (svc *Service) today(s domain.Schedule) domain.Date {
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		loc = time.UTC
	}
	return domain.DateOf(svc.now().In(loc))
}

// transition performs one spec §6 action atomically.
//
// Everything that decides the outcome happens inside a single transaction, in this
// order: lock the schedule, check the precondition against the state the lock
// guarantees is current, apply the change with its audit event, then read back what
// was committed. Splitting any step out reopens the window this exists to close — a
// precondition checked before the lock is checked against a state another caller is
// still free to change, and both callers then write over each other having each been
// individually valid.
//
// When two transitions contend, the second blocks on the lock, then re-reads the row
// the winner committed. Its precondition is evaluated against that new state, so it
// fails as a TransitionError and surfaces as 409 rather than as a second conflicting
// write. That is the whole design: the loser is told no, not merged in.
//
// The locking read is also this package's authorization gate. A schedule belonging to
// another customer comes back as ErrNotFound, so the action is refused before anything
// is touched.
func (svc *Service) transition(
	ctx context.Context,
	scheduleID string,
	a domain.Action,
	caller Caller,
	apply func(tx store.Repository, s domain.Schedule) error,
) (domain.Schedule, error) {
	var out domain.Schedule

	err := svc.repo.InTx(ctx, func(tx store.Repository) error {
		s, err := tx.GetScheduleForUpdate(ctx, scheduleID, caller.Scope)
		if err != nil {
			return err
		}
		if err := domain.ValidateScheduleTransition(s.Status, a); err != nil {
			return err
		}
		if err := apply(tx, s); err != nil {
			return err
		}
		// Read back inside the transaction: the caller is handed the state that was
		// actually committed, not a second read that another writer could land in
		// between.
		out, err = tx.GetSchedule(ctx, scheduleID, caller.Scope)
		return err
	})
	if err != nil {
		return domain.Schedule{}, err
	}
	return out, nil
}

func payload(kv map[string]any) []byte {
	b, err := json.Marshal(kv)
	if err != nil {
		// The maps below hold only strings, ints and dates, so this cannot fail; an
		// empty object keeps the log writable rather than failing the transition.
		return []byte("{}")
	}
	return b
}

// Pause suspends a schedule (spec §6).
//
// Unexecuted occurrences are canceled rather than left planned: a paused schedule that
// still shows upcoming orders is a schedule the customer will reasonably believe is
// still going to charge them.
func (svc *Service) Pause(ctx context.Context, scheduleID string, until *domain.Date, caller Caller) (domain.Schedule, error) {
	return svc.transition(ctx, scheduleID, domain.ActionPause, caller,
		func(tx store.Repository, s domain.Schedule) error {
			if until != nil && !until.After(svc.today(s)) {
				return &domain.TransitionError{
					Action: domain.ActionPause, Status: s.Status,
					Message: "the date to resume on must be in the future",
				}
			}
			if err := tx.UpdateScheduleStatus(ctx, s.ID, domain.SchedulePaused, until); err != nil {
				return err
			}
			canceled, err := tx.CancelUnexecutedOccurrences(ctx, s.ID)
			if err != nil {
				return err
			}
			if err := tx.UpdateScheduleNextRun(ctx, s.ID, nil); err != nil {
				return err
			}
			body := map[string]any{"occurrences_canceled": canceled}
			if until != nil {
				body["paused_until"] = until.String()
			}
			return tx.AppendEvent(ctx, domain.ScheduleEvent{
				ScheduleID: s.ID,
				EventType:  domain.EventSchedulePaused,
				Actor:      caller.Actor,
				Payload:    payload(body),
			})
		})
}

// Resume reactivates a paused schedule, re-anchoring it to today (spec §6).
//
// Re-anchoring is why the customer's next order arrives one interval from now rather
// than on the rhythm they left behind — a schedule paused for six months should not
// resume by immediately charging for the shipments it missed.
func (svc *Service) Resume(ctx context.Context, scheduleID string, caller Caller) (domain.Schedule, error) {
	return svc.transition(ctx, scheduleID, domain.ActionResume, caller,
		func(tx store.Repository, s domain.Schedule) error {
			today := svc.today(s)

			if err := tx.UpdateScheduleStatus(ctx, s.ID, domain.ScheduleActive, nil); err != nil {
				return err
			}
			if err := tx.UpdateScheduleCadence(ctx, s.ID, s.IntervalDays, today); err != nil {
				return err
			}
			if err := tx.AppendEvent(ctx, domain.ScheduleEvent{
				ScheduleID: s.ID,
				EventType:  domain.EventScheduleResumed,
				Actor:      caller.Actor,
				Payload: payload(map[string]any{
					"anchor_date":   today.String(),
					"interval_days": s.IntervalDays,
				}),
			}); err != nil {
				return err
			}

			resumed := s
			resumed.Status = domain.ScheduleActive
			resumed.AnchorDate = today
			resumed.PausedUntil = nil
			_, _, err := svc.mat.WithRepo(tx).Run(ctx, resumed, today)
			return err
		})
}

// SkipNext skips the soonest upcoming order (spec §6).
//
// Only that occurrence changes. Every later one keeps its date, because dates derive
// from the anchor rather than from the occurrence before them — skipping one shipment
// does not slide the rest of the schedule.
func (svc *Service) SkipNext(ctx context.Context, scheduleID, idempotencyKey string, caller Caller) (domain.Schedule, error) {
	if err := domain.ValidateIdempotencyKey(idempotencyKey); err != nil {
		return domain.Schedule{}, err
	}

	return svc.transition(ctx, scheduleID, domain.ActionSkipNext, caller,
		func(tx store.Repository, s domain.Schedule) error {
			// Checked before resolving a target at all. "Next actionable" is not
			// stable across a retry — the first call's skip is exactly what changes
			// which occurrence is next — so a retry that gets this far would skip a
			// second, different occurrence. A prior event under this key means the
			// customer's one request already happened; do nothing further.
			done, err := tx.EventExistsWithKey(ctx, s.ID, domain.EventOccurrenceSkipped, idempotencyKey)
			if err != nil {
				return err
			}
			if done {
				return nil
			}

			// Chosen under the same lock that will move it. Picking the target before
			// the lock would let a concurrent skip take the same occurrence, and the
			// second write would silently skip an order the customer never asked to.
			occ, err := svc.nextOccurrence(ctx, tx, s, domain.ActionSkipNext,
				"there is no upcoming order to skip")
			if err != nil {
				return err
			}

			if err := tx.UpdateOccurrenceStatus(ctx, occ.ID, domain.OccurrenceSkipped); err != nil {
				return err
			}
			if err := tx.AppendEvent(ctx, domain.ScheduleEvent{
				ScheduleID:     s.ID,
				EventType:      domain.EventOccurrenceSkipped,
				Actor:          caller.Actor,
				IdempotencyKey: &idempotencyKey,
				Payload: payload(map[string]any{
					"sequence_no":   occ.SequenceNo,
					"scheduled_for": occ.ScheduledFor.String(),
				}),
			}); err != nil {
				return err
			}
			// Top the horizon back up so the portal still shows a full upcoming queue,
			// and so next_run_date points at the order that is now next.
			_, _, err = svc.mat.WithRepo(tx).Run(ctx, s, svc.today(s))
			return err
		})
}

// Defer pushes the soonest upcoming order back by days (spec §6).
//
// The anchor does not move. That is the deliberate choice in spec §6: a customer who
// pushes one shipment a week out returns to their normal rhythm afterward rather than
// permanently sliding.
func (svc *Service) Defer(ctx context.Context, scheduleID string, days int, idempotencyKey string, caller Caller) (domain.Schedule, error) {
	// Checked before the transaction opens: a request that cannot succeed should not
	// take a lock other callers are waiting on.
	if err := domain.ValidateDeferDays(days); err != nil {
		return domain.Schedule{}, err
	}
	if err := domain.ValidateIdempotencyKey(idempotencyKey); err != nil {
		return domain.Schedule{}, err
	}

	return svc.transition(ctx, scheduleID, domain.ActionDefer, caller,
		func(tx store.Repository, s domain.Schedule) error {
			// See SkipNext: a retry that reached nextOccurrence could shift a second,
			// different occurrence, or shift the same one a second time if it is still
			// soonest after the first shift. Neither is what the customer asked for.
			done, err := tx.EventExistsWithKey(ctx, s.ID, domain.EventOccurrenceDeferred, idempotencyKey)
			if err != nil {
				return err
			}
			if done {
				return nil
			}

			occ, err := svc.nextOccurrence(ctx, tx, s, domain.ActionDefer,
				"there is no upcoming order to move")
			if err != nil {
				return err
			}

			from := occ.ScheduledFor
			to := from.AddDays(days)

			if err := tx.UpdateOccurrenceDate(ctx, occ.ID, to); err != nil {
				return err
			}
			if err := tx.AppendEvent(ctx, domain.ScheduleEvent{
				ScheduleID:     s.ID,
				EventType:      domain.EventOccurrenceDeferred,
				Actor:          caller.Actor,
				IdempotencyKey: &idempotencyKey,
				Payload: payload(map[string]any{
					"sequence_no": occ.SequenceNo,
					"from":        from.String(),
					"to":          to.String(),
					"days":        days,
				}),
			}); err != nil {
				return err
			}
			// The occurrence still exists, so the horizon count is unchanged; only which
			// date comes first may have moved.
			return svc.mat.WithRepo(tx).RefreshNextRunDate(ctx, s, svc.today(s))
		})
}

// ChangeCadence updates the interval and re-anchors the schedule (spec §6).
//
// Re-anchoring to the last placed order is what makes the new interval measure from
// the customer's most recent shipment rather than from an origin they have long since
// passed. Planned occurrences are rewritten; pending ones are left alone, because a
// pending occurrence has already had its pre-billing notice sent (spec §5) and moving
// it would contradict the notice the customer was just given.
func (svc *Service) ChangeCadence(ctx context.Context, scheduleID string, intervalDays int, caller Caller) (domain.Schedule, error) {
	if err := domain.ValidateInterval(intervalDays); err != nil {
		return domain.Schedule{}, err
	}

	return svc.transition(ctx, scheduleID, domain.ActionChangeCadence, caller,
		func(tx store.Repository, s domain.Schedule) error {
			today := svc.today(s)

			// Read under the lock: the anchor this cadence is measured from must be the
			// last order as of the moment the change commits, not as of a read that a
			// concurrent execution could have overtaken.
			anchor := today
			switch last, err := tx.LastPlacedOccurrence(ctx, s.ID); {
			case err == nil:
				anchor = last.ScheduledFor
			case errors.Is(err, store.ErrNotFound):
				// Nothing has shipped yet, so there is no last order to measure from.
			default:
				return err
			}

			if err := tx.UpdateScheduleCadence(ctx, s.ID, intervalDays, anchor); err != nil {
				return err
			}
			rewritten, err := tx.CancelPlannedOccurrences(ctx, s.ID)
			if err != nil {
				return err
			}
			if err := tx.AppendEvent(ctx, domain.ScheduleEvent{
				ScheduleID: s.ID,
				EventType:  domain.EventScheduleCadenceChanged,
				Actor:      caller.Actor,
				Payload: payload(map[string]any{
					"from_interval_days":    s.IntervalDays,
					"to_interval_days":      intervalDays,
					"anchor_date":           anchor.String(),
					"occurrences_rewritten": rewritten,
				}),
			}); err != nil {
				return err
			}

			changed := s
			changed.IntervalDays = intervalDays
			changed.AnchorDate = anchor
			if !changed.IsActive() {
				// A paused schedule keeps its new cadence but plans nothing until it
				// resumes, so there is no horizon to rebuild.
				return tx.UpdateScheduleNextRun(ctx, s.ID, nil)
			}
			_, _, err = svc.mat.WithRepo(tx).Run(ctx, changed, today)
			return err
		})
}

// Cancel ends a schedule, capturing why (spec §6).
//
// The reason code is required and drawn from a closed set: spec §8 projects churn
// analysis off these events, and free text does not aggregate.
func (svc *Service) Cancel(ctx context.Context, scheduleID, reasonCode string, caller Caller) (domain.Schedule, error) {
	if err := domain.ValidateCancellationReason(reasonCode); err != nil {
		return domain.Schedule{}, err
	}

	return svc.transition(ctx, scheduleID, domain.ActionCancel, caller,
		func(tx store.Repository, s domain.Schedule) error {
			if err := tx.UpdateScheduleStatus(ctx, s.ID, domain.ScheduleCanceled, nil); err != nil {
				return err
			}
			canceled, err := tx.CancelUnexecutedOccurrences(ctx, s.ID)
			if err != nil {
				return err
			}
			if err := tx.UpdateScheduleNextRun(ctx, s.ID, nil); err != nil {
				return err
			}
			reason := reasonCode
			return tx.AppendEvent(ctx, domain.ScheduleEvent{
				ScheduleID: s.ID,
				EventType:  domain.EventScheduleCanceled,
				Actor:      caller.Actor,
				ReasonCode: &reason,
				Payload:    payload(map[string]any{"occurrences_canceled": canceled}),
			})
		})
}

// nextOccurrence returns the occurrence a skip or defer targets, translating "none
// found" into a precondition failure the customer can act on.
func (svc *Service) nextOccurrence(ctx context.Context, tx store.Repository, s domain.Schedule, a domain.Action, emptyMsg string) (domain.Occurrence, error) {
	occ, err := tx.NextActionableOccurrence(ctx, s.ID)
	if errors.Is(err, store.ErrNotFound) {
		return domain.Occurrence{}, &domain.TransitionError{Action: a, Status: s.Status, Message: emptyMsg}
	}
	if err != nil {
		return domain.Occurrence{}, fmt.Errorf("find next order: %w", err)
	}
	if err := domain.ValidateOccurrenceAction(occ, a); err != nil {
		return domain.Occurrence{}, err
	}
	return occ, nil
}
