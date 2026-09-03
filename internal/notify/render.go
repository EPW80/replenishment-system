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
	domain.ReasonNoLongerWanted: "no longer needed",
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
}

// eventPayload is the subset of each event type's JSON payload this package reads.
// Only the fields a given event type actually writes will be non-zero; see
// internal/schedule/service.go for the shapes (schedule.paused: occurrences_canceled,
// paused_until; schedule.resumed: anchor_date, interval_days).
type eventPayload struct {
	PausedUntil string `json:"paused_until"`
}

// render produces the subject and HTML body for one claimed event.
func render(e domain.NotifiableEvent, s domain.Schedule, items []domain.ScheduleItem, supportContact string) (subject, body string, err error) {
	tmpl, ok := templates[e.EventType]
	if !ok {
		return "", "", fmt.Errorf("%w: %s", errNoTemplate, e.EventType)
	}

	data := templateData{
		IntervalDays:   s.IntervalDays,
		AnchorDate:     s.AnchorDate.String(),
		SupportContact: supportContact,
	}
	if s.NextRunDate != nil {
		data.NextOrderDate = s.NextRunDate.String()
	}
	for _, it := range items {
		data.Items = append(data.Items, itemData{SKU: it.SKU, Quantity: it.Quantity})
	}

	var p eventPayload
	if len(e.Payload) > 0 {
		// A malformed payload should not stop the send — the schedule fields above
		// already carry everything a template strictly needs, and an event this
		// package itself wrote should never fail to parse. Render with what parsed
		// rather than fail the whole notification over an inessential field.
		_ = json.Unmarshal(e.Payload, &p)
	}
	data.PausedUntil = p.PausedUntil

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
