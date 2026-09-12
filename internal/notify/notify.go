// Package notify sends the Phase 4 transactional emails (spec §7): schedule created,
// paused, resumed, canceled, and the pre-billing notice that spec §5 step 2 requires
// when an occurrence is armed. The two remaining sends (order placed, dunning ladder)
// need Phase 2's order pipeline and are not here.
//
// Delivery is deliberately at-least-once, not exactly-once (docs/adr/0010): a
// duplicate confirmation email is cosmetic, unlike a duplicate occurrence or a
// duplicate skip/defer (docs/adr/0008, docs/adr/0009), where a duplicate corrupts
// state. That is what lets the outbox in internal/store stay simple.
package notify

import (
	"context"
	"encoding/json"
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

// batchSize is how much work one claim page holds. RunAll pages through claims of
// this size until a page comes back short, so this bounds memory and lock duration
// per page rather than capping how much a single RunAll invocation can process.
const batchSize = 200

// notifiableEventTypes are the spec §7 events this package sends for.
var notifiableEventTypes = []string{
	domain.EventScheduleCreated,
	domain.EventSchedulePaused,
	domain.EventScheduleResumed,
	domain.EventScheduleCanceled,
	domain.EventOccurrenceArmed,
}

// eventsNeedingItems are the events whose template lists what is shipping. Spec §7
// asks the pre-billing notice for "what's shipping, when charged", so it needs the
// same item load schedule.created does -- the other three describe the schedule, not
// a shipment, and would only pay for a query they never render.
var eventsNeedingItems = map[string]bool{
	domain.EventScheduleCreated: true,
	domain.EventOccurrenceArmed: true,
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

	// Superseded counts pre-billing notices not sent because the customer acted on
	// the occurrence first — they skipped, deferred past, or canceled it between the
	// arm and this run. Distinct from Skipped, which is about a missing address: this
	// one is the pre-billing window doing exactly what spec §5 step 2 built it for,
	// and is never an error.
	Superseded int
}

// RunAll claims and attempts every outstanding notification, paging through claims
// of batchSize until a page comes back short, so one invocation drains the full
// backlog rather than leaving anything past the first page for tomorrow.
//
// A failure sending one notification does not abort the run — that would let one bad
// address block every other customer's confirmation. Failures are recorded or logged,
// processing continues through the backlog, and RunAll returns an aggregate error so
// cmd/notify's exit code cannot report success while any event failed.
func (d *Dispatcher) RunAll(ctx context.Context) (Result, error) {
	var res Result
	var runErr error
	for {
		events, err := d.repo.ClaimNotifiableEvents(ctx, notifiableEventTypes, visibilityTimeout, batchSize)
		if err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("claim notifiable events: %w", err))
			break
		}
		res.Claimed += len(events)

		for _, e := range events {
			outcome, err := d.dispatchOne(ctx, e)
			if err != nil {
				d.log.Error("dispatch notification failed",
					"schedule_event_id", e.ScheduleEventID, "event_type", e.EventType, "error", err)
				runErr = errors.Join(runErr, err)
				continue
			}
			switch outcome {
			case outcomeSent:
				res.Sent++
			case outcomeSkipped:
				res.Skipped++
			case outcomeSuperseded:
				res.Superseded++
			case outcomeSendFailed:
				res.SendFailed++
				runErr = errors.Join(runErr, fmt.Errorf("notification send failed for schedule event %d", e.ScheduleEventID))
			}
		}

		if len(events) < batchSize {
			break
		}
	}
	return res, runErr
}

type outcome int

const (
	outcomeSent outcome = iota
	outcomeSkipped
	outcomeSuperseded
	outcomeSendFailed
)

// dispatchOne resolves one claimed event to a sent, skipped, or failed outcome.
//
// The returned error is only for an infrastructure problem — the schedule could not
// be read, the render failed, or notification_log could not be updated — never for a
// send that Postmark itself rejected. That case is recorded as outcomeSendFailed;
// RunAll converts the outcome into an aggregate error after continuing the batch.
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

	// The pre-billing notice states a charge that has not happened yet, so it is
	// resolved against the occurrence as it stands now rather than as the event
	// recorded it. This is a deliberate exception to the snapshot rule the other four
	// emails follow (see render.applyEventSnapshot): they are past-tense confirmations,
	// true forever once recorded, while this one is a claim about the future that the
	// customer is invited to falsify. Arming and dispatch are separate scheduled tasks,
	// so a customer can skip or defer in between -- which is precisely what spec §5
	// step 2 opens the window for.
	var occ *domain.Occurrence
	if e.EventType == domain.EventOccurrenceArmed {
		current, resolveErr := d.armedOccurrence(ctx, e)
		if resolveErr != nil {
			return 0, resolveErr
		}
		if current.Status != domain.OccurrencePending {
			// Skipped, deferred past the window, canceled, or already placed. Sending
			// now would announce a charge that is not coming. Resolve the outbox row
			// so it is not reconsidered every run, exactly as the no-address path does.
			if err := d.repo.MarkNotificationSent(ctx, e.ScheduleEventID, d.now()); err != nil {
				return 0, fmt.Errorf("mark superseded: %w", err)
			}
			return outcomeSuperseded, nil
		}
		occ = &current
	}

	var items []domain.ScheduleItem
	if eventsNeedingItems[e.EventType] {
		items, err = d.repo.ListScheduleItems(ctx, e.ScheduleID, store.SystemScope())
		if err != nil {
			return 0, fmt.Errorf("list items for %s: %w", e.ScheduleID, err)
		}
	}

	subject, body, err := render(e, s, items, occ, d.supportContact)
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

// armedOccurrence resolves the occurrence an occurrence.armed event refers to, as it
// stands now.
//
// The event's payload carries sequence_no, and (schedule_id, sequence_no) is UNIQUE,
// so it identifies the occurrence stably even after a defer moves its date. A payload
// without it, or a sequence number matching no row, is an infrastructure error rather
// than a reason to send: the alternative is guessing which order the notice is about,
// and guessing wrong means telling a customer about a charge that is not theirs.
func (d *Dispatcher) armedOccurrence(ctx context.Context, e domain.NotifiableEvent) (domain.Occurrence, error) {
	var snapshot struct {
		SequenceNo *int `json:"sequence_no"`
	}
	if err := json.Unmarshal(e.Payload, &snapshot); err != nil {
		return domain.Occurrence{}, fmt.Errorf("decode armed payload for event %d: %w", e.ScheduleEventID, err)
	}
	if snapshot.SequenceNo == nil {
		return domain.Occurrence{}, fmt.Errorf("armed event %d has no sequence_no", e.ScheduleEventID)
	}

	occ, err := d.repo.GetOccurrenceBySequence(ctx, e.ScheduleID, *snapshot.SequenceNo)
	if err != nil {
		return domain.Occurrence{}, fmt.Errorf("get occurrence %s#%d: %w", e.ScheduleID, *snapshot.SequenceNo, err)
	}
	return occ, nil
}

// errNoTemplate is wrapped with the event type so a future event type added to
// notifiableEventTypes without a matching template fails loudly instead of silently.
var errNoTemplate = errors.New("no template registered for event type")
