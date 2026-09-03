// Package notify sends the Phase 4 transactional emails (spec §7): schedule created,
// paused, resumed, and canceled. The three billing-related sends (pre-billing notice,
// order placed, dunning ladder) need Phase 2's order pipeline and are not here.
//
// Delivery is deliberately at-least-once, not exactly-once (docs/adr/0010): a
// duplicate confirmation email is cosmetic, unlike a duplicate occurrence or a
// duplicate skip/defer (docs/adr/0008, docs/adr/0009), where a duplicate corrupts
// state. That is what lets the outbox in internal/store stay simple.
package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/EPW80/replenishment-system/internal/domain"
	"github.com/EPW80/replenishment-system/internal/store"
)

// Sender delivers one rendered email. PostmarkSender is the production
// implementation; tests use a stub.
type Sender interface {
	Send(ctx context.Context, to, subject, htmlBody string) error
}

// visibilityTimeout bounds how long a claimed-but-unresolved notification is treated
// as still in flight before another run may reclaim it (see ClaimNotifiableEvents).
// cmd/notify is a short one-shot process, so a claim that outlives this was almost
// certainly abandoned by a crash, not a slow send.
const visibilityTimeout = 15 * time.Minute

// maxAttempts caps retries on a send that keeps failing, so one bad address cannot
// retry forever and crowd out newer notifications.
const maxAttempts = 5

// batchSize is how much work one RunAll claims. Generous for a nightly volume of
// pause/resume/cancel/create events; revisit if that assumption stops holding.
const batchSize = 200

// notifiableEventTypes are the four spec §7 events this package sends for.
var notifiableEventTypes = []string{
	domain.EventScheduleCreated,
	domain.EventSchedulePaused,
	domain.EventScheduleResumed,
	domain.EventScheduleCanceled,
}

// Dispatcher sends outstanding notifications and records the outcome of each.
type Dispatcher struct {
	repo           store.Repository
	sender         Sender
	supportContact string
	log            *slog.Logger

	// now is injected so tests can assert on sent_at without a real clock.
	now func() time.Time
}

// New returns a Dispatcher. supportContact is the "contact us" line every template
// carries — there is no portal link, since Phase 5 does not exist in this repo yet.
func New(repo store.Repository, sender Sender, supportContact string, log *slog.Logger) *Dispatcher {
	if log == nil {
		log = slog.Default()
	}
	return &Dispatcher{repo: repo, sender: sender, supportContact: supportContact, log: log, now: time.Now}
}

// Result reports what one run did.
type Result struct {
	Claimed    int
	Sent       int
	Skipped    int // no customer_email on file — nothing to send, not a failure
	SendFailed int // Postmark rejected or errored; recorded in notification_log
}

// RunAll claims outstanding work and attempts each. A failure sending one
// notification does not abort the run — that would let one bad address block every
// other customer's confirmation. Failures are logged and recorded (MarkNotificationFailed);
// RunAll itself returns an error only for a claim or bookkeeping failure severe enough
// that no notifications could be processed at all.
func (d *Dispatcher) RunAll(ctx context.Context) (Result, error) {
	events, err := d.repo.ClaimNotifiableEvents(ctx, notifiableEventTypes, visibilityTimeout, batchSize)
	if err != nil {
		return Result{}, fmt.Errorf("claim notifiable events: %w", err)
	}

	res := Result{Claimed: len(events)}
	for _, e := range events {
		outcome, err := d.dispatchOne(ctx, e)
		if err != nil {
			d.log.Error("dispatch notification failed",
				"schedule_event_id", e.ScheduleEventID, "event_type", e.EventType, "error", err)
			continue
		}
		switch outcome {
		case outcomeSent:
			res.Sent++
		case outcomeSkipped:
			res.Skipped++
		case outcomeSendFailed:
			res.SendFailed++
		}
	}
	return res, nil
}

type outcome int

const (
	outcomeSent outcome = iota
	outcomeSkipped
	outcomeSendFailed
)

// dispatchOne resolves one claimed event to a sent, skipped, or failed outcome.
//
// The returned error is only for an infrastructure problem — the schedule could not
// be read, the render failed, or notification_log could not be updated — never for a
// send that Postmark itself rejected. That case is a normal, expected outcome
// (outcomeSendFailed) recorded via MarkNotificationFailed, not a Go error: a batch of
// a hundred events must not stop because one address bounced.
func (d *Dispatcher) dispatchOne(ctx context.Context, e domain.NotifiableEvent) (outcome, error) {
	s, err := d.repo.GetSchedule(ctx, e.ScheduleID, store.SystemScope())
	if err != nil {
		return 0, fmt.Errorf("get schedule %s: %w", e.ScheduleID, err)
	}

	// customer_email is fetched fresh here rather than carried on the event: a
	// customer who updates their address after the event was recorded still gets it
	// right (docs/adr/0010).
	if s.CustomerEmail == "" {
		// A schedule created before Phase 4, or a caller that never sent one. Nothing
		// to send, and not a failure — mark it resolved so it is not reconsidered
		// every run.
		if err := d.repo.MarkNotificationSent(ctx, e.ScheduleEventID, d.now()); err != nil {
			return 0, fmt.Errorf("mark skipped: %w", err)
		}
		return outcomeSkipped, nil
	}

	var items []domain.ScheduleItem
	if e.EventType == domain.EventScheduleCreated {
		items, err = d.repo.ListScheduleItems(ctx, e.ScheduleID, store.SystemScope())
		if err != nil {
			return 0, fmt.Errorf("list items for %s: %w", e.ScheduleID, err)
		}
	}

	subject, body, err := render(e, s, items, d.supportContact)
	if err != nil {
		return 0, fmt.Errorf("render %s: %w", e.EventType, err)
	}

	if sendErr := d.sender.Send(ctx, s.CustomerEmail, subject, body); sendErr != nil {
		if err := d.repo.MarkNotificationFailed(ctx, e.ScheduleEventID, sendErr.Error(), maxAttempts); err != nil {
			return 0, fmt.Errorf("mark failed: %w", err)
		}
		return outcomeSendFailed, nil
	}

	if err := d.repo.MarkNotificationSent(ctx, e.ScheduleEventID, d.now()); err != nil {
		return 0, fmt.Errorf("mark sent: %w", err)
	}
	return outcomeSent, nil
}

// errNoTemplate is wrapped with the event type so a future event type added to
// notifiableEventTypes without a matching template fails loudly instead of silently.
var errNoTemplate = errors.New("no template registered for event type")
