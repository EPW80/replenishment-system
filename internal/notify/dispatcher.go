package notify

import (
	"context"
	"log/slog"

	"github.com/EPW80/replenishment-system/internal/domain"
	"github.com/EPW80/replenishment-system/internal/store"
)

// Dispatcher is a transactional outbox off schedule_events — the same append-only
// source of truth the spec §8 read models already project from (internal/readmodel).
// It is not a new delivery mechanism, just another projection of that log.
//
// Deliberately decoupled from internal/schedule's transition handlers: those commit
// a state change inside a database transaction, and an external HTTP call inside
// that same transaction would let a slow or down email provider block or fail a
// customer's pause request for a reason that has nothing to do with the pause
// itself. See docs/adr/0007.
type Dispatcher struct {
	Repo         store.Repository
	Sender       Sender
	SupportEmail string
	Log          *slog.Logger
}

// Result reports what one dispatch pass did.
type Result struct {
	Sent   int
	Failed int
}

// RunOnce sends every not-yet-notified event of the four types this phase covers.
//
// One failing send does not stop the batch — the same reasoning as
// materialize.Materializer.RunAll: a single customer's bounced address or a
// transient Postmark error must not stall notifications for everyone else. A
// failed send is not recorded, so it is picked up again on the next run — see
// docs/adr/0007 on why at-least-once delivery is the deliberate choice here.
func (d *Dispatcher) RunOnce(ctx context.Context) (Result, error) {
	log := d.Log
	if log == nil {
		log = slog.Default()
	}

	events, err := d.Repo.UnnotifiedEvents(ctx, NotifiableEventTypes())
	if err != nil {
		return Result{}, err
	}

	var res Result
	for _, e := range events {
		if err := d.dispatchOne(ctx, e); err != nil {
			res.Failed++
			log.Error("notification dispatch failed", "event_id", e.ID, "event_type", e.EventType, "error", err)
			continue
		}
		res.Sent++
	}
	return res, nil
}

func (d *Dispatcher) dispatchOne(ctx context.Context, e domain.ScheduleEvent) error {
	s, err := d.Repo.GetSchedule(ctx, e.ScheduleID)
	if err != nil {
		return err
	}

	// Computed directly from the schedule's current anchor rather than read from
	// next_run_date: a freshly created schedule has no materialized occurrence yet
	// (that's a separate nightly job, cmd/materialize), and this must not depend on
	// which job happens to run first. Occurrence 1 from the current anchor is
	// always the next order, including immediately after a resume re-anchors to
	// today. See docs/adr/0007.
	nextOrder, dateErr := domain.OccurrenceDate(s.AnchorDate, s.IntervalDays, 1)
	nextOrderDate := ""
	if dateErr == nil {
		nextOrderDate = nextOrder.String()
	}

	subject, htmlBody, err := render(e.EventType, TemplateData{
		NextOrderDate: nextOrderDate,
		IntervalDays:  s.IntervalDays,
		SupportEmail:  d.SupportEmail,
	})
	if err != nil {
		return err
	}

	if err := d.Sender.Send(ctx, Message{
		To:       s.CustomerEmail,
		Subject:  subject,
		HTMLBody: htmlBody,
		TextBody: htmlToText(htmlBody),
	}); err != nil {
		return err
	}

	// Recorded only after a successful send. A crash between the two could in
	// theory resend once on the next run — deliberate, documented in docs/adr/0007,
	// and a proportionate trade-off: a duplicate confirmation email is a minor
	// annoyance, unlike a duplicate charge, which the occurrence idempotency key
	// (spec §3) cannot tolerate at all.
	return d.Repo.RecordNotification(ctx, e.ID, e.EventType)
}
