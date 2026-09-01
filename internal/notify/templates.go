package notify

import (
	"bytes"
	"embed"
	"fmt"
	htmlpkg "html"
	"html/template"
	"regexp"
	"strings"

	"github.com/EPW80/replenishment-system/internal/domain"
)

// templateFS embeds the templates into the binary, same reasoning as the migrations
// embed in internal/store/migrate.go: the deployed binary carries the exact copy
// that was reviewed with it.
//
//go:embed templates/*.tmpl
var templateFS embed.FS

var templates = template.Must(template.ParseFS(templateFS, "templates/*.tmpl"))

// TemplateData is what every template can reference. Deliberately narrow: spec §2's
// compliance boundary applies to every field here as much as to the copy itself —
// nothing about usage, timing of use, or supply belongs on this struct.
type TemplateData struct {
	// NextOrderDate is the next scheduled order, computed directly from the
	// schedule's current anchor rather than read from next_run_date — see
	// docs/adr/0007. Empty for events (paused, canceled) that don't need it.
	NextOrderDate string
	IntervalDays  int
	SupportEmail  string
}

// eventTemplates maps each notifiable event type to the template names defined in
// internal/notify/templates/*.tmpl. Only spec §7 sends with nothing missing from
// Phase 2 are here — see the package comment.
var eventTemplates = map[string]struct{ subject, body string }{
	domain.EventScheduleCreated:  {"schedule_created_subject", "schedule_created_body"},
	domain.EventSchedulePaused:   {"schedule_paused_subject", "schedule_paused_body"},
	domain.EventScheduleResumed:  {"schedule_resumed_subject", "schedule_resumed_body"},
	domain.EventScheduleCanceled: {"schedule_canceled_subject", "schedule_canceled_body"},
}

// NotifiableEventTypes lists the event types eventTemplates covers, in the shape
// store.Repository.UnnotifiedEvents wants — the single place the dispatcher's query
// and the template set stay in sync.
func NotifiableEventTypes() []string {
	types := make([]string, 0, len(eventTemplates))
	for t := range eventTemplates {
		types = append(types, t)
	}
	return types
}

// render produces the subject and HTML body for one event type. TextBody is
// deliberately not templated separately — see Dispatcher, which strips the HTML
// for the plain-text part rather than maintaining two copies of every message that
// would inevitably drift.
func render(eventType string, data TemplateData) (subject, htmlBody string, err error) {
	names, ok := eventTemplates[eventType]
	if !ok {
		return "", "", fmt.Errorf("no template for event type %q", eventType)
	}

	var subjectBuf, bodyBuf bytes.Buffer
	if err := templates.ExecuteTemplate(&subjectBuf, names.subject, data); err != nil {
		return "", "", fmt.Errorf("render subject for %q: %w", eventType, err)
	}
	if err := templates.ExecuteTemplate(&bodyBuf, names.body, data); err != nil {
		return "", "", fmt.Errorf("render body for %q: %w", eventType, err)
	}
	return subjectBuf.String(), bodyBuf.String(), nil
}

var (
	htmlBlockBoundary = regexp.MustCompile(`(?i)</p>|<br\s*/?>`)
	htmlAnyTag        = regexp.MustCompile(`<[^>]*>`)
	blankLines        = regexp.MustCompile(`\n{3,}`)
)

// htmlToText produces a plain-text fallback from the templates' HTML output.
//
// This is safe only because the input is always our own template output, never
// third-party HTML: every template here uses <p> and nothing else, so a full HTML
// parser would be more machinery than the actual shape of the content justifies.
// If a template ever needs richer markup, this needs to become a real parser
// instead of gaining more regexes.
func htmlToText(html string) string {
	text := htmlBlockBoundary.ReplaceAllString(html, "\n\n")
	text = htmlAnyTag.ReplaceAllString(text, "")
	text = htmlpkg.UnescapeString(text)
	text = blankLines.ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
}
