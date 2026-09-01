package notify

import (
	"strings"
	"testing"

	"github.com/EPW80/replenishment-system/internal/domain"
)

// Every event type this phase covers must render a non-empty subject and body,
// and — this is the point of the test, not a formality — none of them may drift
// into the language spec §2 forbids. internal/compliance's guard scans identifiers,
// not template prose, so this is the only mechanical check this copy gets.
func TestRenderAllEventTypesProduceCompliantCopy(t *testing.T) {
	data := TemplateData{
		NextOrderDate: "2026-03-15",
		IntervalDays:  30,
		SupportEmail:  "support@cadenceos.example",
	}

	forbidden := []string{
		"dose", "dosage", "per day", "per week", "remaining", "days left",
		"adherence", "intake", "supply", "symptom", "recommended dose",
		"when to take", "take your", "run out",
	}

	for _, eventType := range NotifiableEventTypes() {
		t.Run(eventType, func(t *testing.T) {
			subject, body, err := render(eventType, data)
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			if subject == "" {
				t.Error("empty subject")
			}
			if body == "" {
				t.Error("empty body")
			}

			lower := strings.ToLower(subject + " " + body)
			for _, bad := range forbidden {
				if strings.Contains(lower, bad) {
					t.Errorf("copy contains %q — spec §2 forbids it\nsubject: %s\nbody: %s", bad, subject, body)
				}
			}
			// Copy rule: "when to reorder," never "when to take." Every template
			// talks about the order/schedule, not the product.
			if !strings.Contains(lower, "order") && !strings.Contains(lower, "schedule") {
				t.Errorf("copy mentions neither 'order' nor 'schedule' — %q", body)
			}
		})
	}
}

func TestRenderUnknownEventType(t *testing.T) {
	if _, _, err := render("not.a.real.event", TemplateData{}); err == nil {
		t.Fatal("expected an error for an unmapped event type")
	}
}

func TestNotifiableEventTypesMatchesTheSpecSevenScope(t *testing.T) {
	want := map[string]bool{
		domain.EventScheduleCreated:  true,
		domain.EventSchedulePaused:   true,
		domain.EventScheduleResumed:  true,
		domain.EventScheduleCanceled: true,
	}
	got := NotifiableEventTypes()
	if len(got) != len(want) {
		t.Fatalf("got %d event types, want %d: %v", len(got), len(want), got)
	}
	for _, t2 := range got {
		if !want[t2] {
			t.Errorf("unexpected event type %q — Phase 4 only covers the four unblocked sends", t2)
		}
	}
}

func TestHTMLToText(t *testing.T) {
	html := "<p>Hello &amp; welcome.</p>\n<p>Second paragraph.</p>"
	got := htmlToText(html)
	if strings.Contains(got, "<p>") || strings.Contains(got, "</p>") {
		t.Errorf("tags survived stripping: %q", got)
	}
	if !strings.Contains(got, "Hello & welcome.") {
		t.Errorf("entity not unescaped or text lost: %q", got)
	}
	if !strings.Contains(got, "Second paragraph.") {
		t.Errorf("second paragraph lost: %q", got)
	}
}
