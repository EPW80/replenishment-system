package notify

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"

	"github.com/EPW80/replenishment-system/internal/domain"
)

// templateFS embeds the email templates into the binary, the same reasoning as
// internal/store's migrationFS: the deployed artifact carries exactly the copy that
// was reviewed with it.
//
//go:embed templates/*.html
var templateFS embed.FS

// templates maps an event type to the template file that renders it. A named
// "subject" and "body" definition in each file is rendered separately, so Postmark
// gets a plain subject line and an HTML body rather than one blob.
var templates = map[string]*template.Template{
	domain.EventScheduleCreated:  template.Must(template.ParseFS(templateFS, "templates/schedule_created.html")),
	domain.EventSchedulePaused:   template.Must(template.ParseFS(templateFS, "templates/schedule_paused.html")),
	domain.EventScheduleResumed:  template.Must(template.ParseFS(templateFS, "templates/schedule_resumed.html")),
	domain.EventScheduleCanceled: template.Must(template.ParseFS(templateFS, "templates/schedule_canceled.html")),
	domain.EventOccurrenceArmed:  template.Must(template.ParseFS(templateFS, "templates/occurrence_armed.html")),
}

// cancellationReasonText turns a closed-set reason code (internal/domain/transitions.go)
// into customer-facing prose. Never the raw code — spec §2's copy rule applies to this
// package exactly as it does to every other customer-facing surface.
var cancellationReasonText = map[string]string{
	domain.ReasonTooExpensive:   "too expensive",
	domain.ReasonTooFrequent:    "orders were too frequent",
	domain.ReasonSwitchedBrand:  "switched to another brand",
	domain.ReasonDeliveryIssue:  "a delivery issue",
	domain.ReasonPaymentIssue:   "a payment issue",
	domain.ReasonNoLongerWanted: "no longer wanted",
	domain.ReasonOther:          "",
}

// itemData is one line of the schedule.created template's item list.
type itemData struct {
	SKU      string
	Quantity int
}

// templateData is every field a template may reference. Not every field is set for
// every event type — an unused field simply renders as its zero value, and each
// template only references the ones relevant to it.
type templateData struct {
	IntervalDays   int
	AnchorDate     string
	NextOrderDate  string // "" if none is currently planned
	Items          []itemData
	PausedUntil    string // "" means paused indefinitely
	ReasonText     string // "" renders no parenthetical in schedule_canceled
	SupportContact string

	// ChargeDate is the date the armed order will be charged and placed, read from
	// the occurrence as it stands at send time rather than from the event snapshot.
	// Set only for occurrence.armed; see render's note on the exception.
	ChargeDate string
}

// render produces the subject and HTML body for one claimed event.
//
// occ is non-nil only for occurrence.armed, and only after the caller has confirmed it
// is still pending. Its date is used in preference to anything in the event payload:
// the pre-billing notice is a statement about a charge that has not happened yet, so it
// must describe the occurrence as it stands now, not as it stood when armed. See the
// note on applyEventSnapshot for why that is the opposite of what the other four
// templates do.
func render(e domain.NotifiableEvent, s domain.Schedule, items []domain.ScheduleItem, occ *domain.Occurrence, supportContact string) (subject, body string, err error) {
	tmpl, ok := templates[e.EventType]
	if !ok {
		return "", "", fmt.Errorf("%w: %s", errNoTemplate, e.EventType)
	}

	data := templateData{
		IntervalDays:   s.IntervalDays,
		AnchorDate:     s.AnchorDate.String(),
		SupportContact: supportContact,
	}
	if occ != nil {
		data.ChargeDate = occ.ScheduledFor.String()
	}
	if s.NextRunDate != nil {
		data.NextOrderDate = s.NextRunDate.String()
	}
	for _, it := range items {
		data.Items = append(data.Items, itemData{SKU: it.SKU, Quantity: it.Quantity})
	}
	// Start with the current row as a fallback for legacy events. applyEventSnapshot
	// then replaces transition-specific fields for newer events, so delayed mail
	// describes the state change that actually caused it.
	if s.PausedUntil != nil {
		data.PausedUntil = s.PausedUntil.String()
	}
	if err := applyEventSnapshot(&data, e); err != nil {
		return "", "", err
	}

	if e.ReasonCode != nil {
		data.ReasonText = cancellationReasonText[*e.ReasonCode]
	}

	var subjectBuf, bodyBuf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&subjectBuf, "subject", data); err != nil {
		return "", "", fmt.Errorf("render subject: %w", err)
	}
	if err := tmpl.ExecuteTemplate(&bodyBuf, "body", data); err != nil {
		return "", "", fmt.Errorf("render body: %w", err)
	}
	return subjectBuf.String(), bodyBuf.String(), nil
}

// applyEventSnapshot replaces mutable schedule fields with the values captured by
// the event. Empty legacy payloads fall back to the current row so pre-snapshot
// events remain deliverable.
//
// occurrence.armed has no case here on purpose, and the omission is load-bearing
// rather than an oversight. The four events below are past-tense confirmations — a
// schedule *was* paused, and a delayed email should still say what it was paused to.
// The pre-billing notice is future-tense: it promises a charge the customer is
// explicitly invited to stop, and arming and dispatch are separate scheduled tasks, so
// they can skip or defer in between. Rendering its date from this snapshot would state
// a charge that is no longer coming. Dispatcher.dispatchOne re-reads the live
// occurrence instead, and declines to send at all when it is no longer pending. Do not
// "restore consistency" by adding a case here.
func applyEventSnapshot(data *templateData, e domain.NotifiableEvent) error {
	if len(e.Payload) == 0 || string(e.Payload) == "{}" {
		return nil
	}

	var snapshot map[string]json.RawMessage
	if err := json.Unmarshal(e.Payload, &snapshot); err != nil {
		return fmt.Errorf("decode event snapshot: %w", err)
	}
	if len(snapshot) == 0 {
		return nil
	}

	readString := func(key string, dst *string) error {
		raw, ok := snapshot[key]
		if !ok {
			return nil
		}
		if err := json.Unmarshal(raw, dst); err != nil {
			return fmt.Errorf("decode event snapshot field %s: %w", key, err)
		}
		return nil
	}
	readInt := func(key string, dst *int) error {
		raw, ok := snapshot[key]
		if !ok {
			return nil
		}
		if err := json.Unmarshal(raw, dst); err != nil {
			return fmt.Errorf("decode event snapshot field %s: %w", key, err)
		}
		return nil
	}

	switch e.EventType {
	case domain.EventScheduleCreated, domain.EventScheduleResumed:
		if err := readInt("interval_days", &data.IntervalDays); err != nil {
			return err
		}
		if err := readString("anchor_date", &data.AnchorDate); err != nil {
			return err
		}
		if err := readString("next_order_date", &data.NextOrderDate); err != nil {
			return err
		}
	case domain.EventSchedulePaused:
		data.PausedUntil = ""
		if err := readString("paused_until", &data.PausedUntil); err != nil {
			return err
		}
	}
	return nil
}
