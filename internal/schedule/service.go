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

// load fetches a schedule and checks the action against its status (spec §6).
func (svc *Service) load(ctx context.Context, scheduleID string, a domain.Action) (domain.Schedule, error) {
	s, err := svc.repo.GetSchedule(ctx, scheduleID)
	if err != nil {
		return domain.Schedule{}, err
	}
	if err := domain.ValidateScheduleTransition(s.Status, a); err != nil {
		return domain.Schedule{}, err
	}
	return s, nil
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
func (svc *Service) Pause(ctx context.Context, scheduleID string, until *domain.Date, actor domain.EventActor) (domain.Schedule, error) {
	s, err := svc.load(ctx, scheduleID, domain.ActionPause)
	if err != nil {
		return domain.Schedule{}, err
	}
	if until != nil && !until.After(svc.today(s)) {
		return domain.Schedule{}, &domain.TransitionError{
			Action: domain.ActionPause, Status: s.Status,
			Message: "the date to resume on must be in the future",
		}
	}

	err = svc.repo.InTx(ctx, func(tx store.Repository) error {
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
			Actor:      actor,
			Payload:    payload(body),
		})
	})
	if err != nil {
		return domain.Schedule{}, err
	}
	return svc.repo.GetSchedule(ctx, scheduleID)
}

// Resume reactivates a paused schedule, re-anchoring it to today (spec §6).
//
// Re-anchoring is why the customer's next order arrives one interval from now rather
// than on the rhythm they left behind — a schedule paused for six months should not
// resume by immediately charging for the shipments it missed.
func (svc *Service) Resume(ctx context.Context, scheduleID string, actor domain.EventActor) (domain.Schedule, error) {
	s, err := svc.load(ctx, scheduleID, domain.ActionResume)
	if err != nil {
		return domain.Schedule{}, err
	}
	today := svc.today(s)

	err = svc.repo.InTx(ctx, func(tx store.Repository) error {
		if err := tx.UpdateScheduleStatus(ctx, s.ID, domain.ScheduleActive, nil); err != nil {
			return err
		}
		if err := tx.UpdateScheduleCadence(ctx, s.ID, s.IntervalDays, today); err != nil {
			return err
		}
		if err := tx.AppendEvent(ctx, domain.ScheduleEvent{
			ScheduleID: s.ID,
			EventType:  domain.EventScheduleResumed,
			Actor:      actor,
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
	if err != nil {
		return domain.Schedule{}, err
	}
	return svc.repo.GetSchedule(ctx, scheduleID)
}

// SkipNext skips the soonest upcoming order (spec §6).
//
// Only that occurrence changes. Every later one keeps its date, because dates derive
// from the anchor rather than from the occurrence before them — skipping one shipment
// does not slide the rest of the schedule.
func (svc *Service) SkipNext(ctx context.Context, scheduleID string, actor domain.EventActor) (domain.Schedule, error) {
	s, err := svc.load(ctx, scheduleID, domain.ActionSkipNext)
	if err != nil {
		return domain.Schedule{}, err
	}
	occ, err := svc.nextOccurrence(ctx, s, domain.ActionSkipNext, "there is no upcoming order to skip")
	if err != nil {
		return domain.Schedule{}, err
	}
	today := svc.today(s)

	err = svc.repo.InTx(ctx, func(tx store.Repository) error {
		if err := tx.UpdateOccurrenceStatus(ctx, occ.ID, domain.OccurrenceSkipped); err != nil {
			return err
		}
		if err := tx.AppendEvent(ctx, domain.ScheduleEvent{
			ScheduleID: s.ID,
			EventType:  domain.EventOccurrenceSkipped,
			Actor:      actor,
			Payload: payload(map[string]any{
				"sequence_no":   occ.SequenceNo,
				"scheduled_for": occ.ScheduledFor.String(),
			}),
		}); err != nil {
			return err
		}
		// Top the horizon back up so the portal still shows a full upcoming queue,
		// and so next_run_date points at the order that is now next.
		_, _, err := svc.mat.WithRepo(tx).Run(ctx, s, today)
		return err
	})
	if err != nil {
		return domain.Schedule{}, err
	}
	return svc.repo.GetSchedule(ctx, scheduleID)
}

// Defer pushes the soonest upcoming order back by days (spec §6).
//
// The anchor does not move. That is the deliberate choice in spec §6: a customer who
// pushes one shipment a week out returns to their normal rhythm afterward rather than
// permanently sliding.
func (svc *Service) Defer(ctx context.Context, scheduleID string, days int, actor domain.EventActor) (domain.Schedule, error) {
	s, err := svc.load(ctx, scheduleID, domain.ActionDefer)
	if err != nil {
		return domain.Schedule{}, err
	}
	if err := domain.ValidateDeferDays(days); err != nil {
		return domain.Schedule{}, err
	}
	occ, err := svc.nextOccurrence(ctx, s, domain.ActionDefer, "there is no upcoming order to move")
	if err != nil {
		return domain.Schedule{}, err
	}

	from := occ.ScheduledFor
	to := from.AddDays(days)
	today := svc.today(s)

	err = svc.repo.InTx(ctx, func(tx store.Repository) error {
		if err := tx.UpdateOccurrenceDate(ctx, occ.ID, to); err != nil {
			return err
		}
		if err := tx.AppendEvent(ctx, domain.ScheduleEvent{
			ScheduleID: s.ID,
			EventType:  domain.EventOccurrenceDeferred,
			Actor:      actor,
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
		return svc.mat.WithRepo(tx).RefreshNextRunDate(ctx, s, today)
	})
	if err != nil {
		return domain.Schedule{}, err
	}
	return svc.repo.GetSchedule(ctx, scheduleID)
}

// ChangeCadence updates the interval and re-anchors the schedule (spec §6).
//
// Re-anchoring to the last placed order is what makes the new interval measure from
// the customer's most recent shipment rather than from an origin they have long since
// passed. Planned occurrences are rewritten; pending ones are left alone, because a
// pending occurrence has already had its pre-billing notice sent (spec §5) and moving
// it would contradict the notice the customer was just given.
func (svc *Service) ChangeCadence(ctx context.Context, scheduleID string, intervalDays int, actor domain.EventActor) (domain.Schedule, error) {
	s, err := svc.load(ctx, scheduleID, domain.ActionChangeCadence)
	if err != nil {
		return domain.Schedule{}, err
	}
	if err := domain.ValidateInterval(intervalDays); err != nil {
		return domain.Schedule{}, err
	}
	today := svc.today(s)

	anchor := today
	switch last, err := svc.repo.LastPlacedOccurrence(ctx, s.ID); {
	case err == nil:
		anchor = last.ScheduledFor
	case errors.Is(err, store.ErrNotFound):
		// Nothing has shipped yet, so there is no last order to measure from.
	default:
		return domain.Schedule{}, err
	}

	err = svc.repo.InTx(ctx, func(tx store.Repository) error {
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
			Actor:      actor,
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
	if err != nil {
		return domain.Schedule{}, err
	}
	return svc.repo.GetSchedule(ctx, scheduleID)
}

// Cancel ends a schedule, capturing why (spec §6).
//
// The reason code is required and drawn from a closed set: spec §8 projects churn
// analysis off these events, and free text does not aggregate.
func (svc *Service) Cancel(ctx context.Context, scheduleID, reasonCode string, actor domain.EventActor) (domain.Schedule, error) {
	s, err := svc.load(ctx, scheduleID, domain.ActionCancel)
	if err != nil {
		return domain.Schedule{}, err
	}
	if err := domain.ValidateCancellationReason(reasonCode); err != nil {
		return domain.Schedule{}, err
	}

	err = svc.repo.InTx(ctx, func(tx store.Repository) error {
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
			Actor:      actor,
			ReasonCode: &reason,
			Payload:    payload(map[string]any{"occurrences_canceled": canceled}),
		})
	})
	if err != nil {
		return domain.Schedule{}, err
	}
	return svc.repo.GetSchedule(ctx, scheduleID)
}

// nextOccurrence returns the occurrence a skip or defer targets, translating "none
// found" into a precondition failure the customer can act on.
func (svc *Service) nextOccurrence(ctx context.Context, s domain.Schedule, a domain.Action, emptyMsg string) (domain.Occurrence, error) {
	occ, err := svc.repo.NextActionableOccurrence(ctx, s.ID)
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
