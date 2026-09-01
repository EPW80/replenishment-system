package notify_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EPW80/replenishment-system/internal/domain"
	"github.com/EPW80/replenishment-system/internal/notify"
	"github.com/EPW80/replenishment-system/internal/store"
	"github.com/EPW80/replenishment-system/internal/testsupport"
)

// fakeSender records every message it's asked to send, and can be told to fail for
// specific recipients — the dispatcher tests use this to prove that one bad send
// does not stop the batch.
type fakeSender struct {
	mu      sync.Mutex
	sent    []notify.Message
	failFor map[string]bool
}

func (f *fakeSender) Send(ctx context.Context, msg notify.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failFor[msg.To] {
		return errors.New("simulated send failure")
	}
	f.sent = append(f.sent, msg)
	return nil
}

func (f *fakeSender) sentTo(addr string) (notify.Message, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.sent {
		if m.To == addr {
			return m, true
		}
	}
	return notify.Message{}, false
}

func newSchedule(t *testing.T, repo *store.PostgresRepository, email string) domain.Schedule {
	t.Helper()
	s := domain.Schedule{
		ID:            uuid.NewString(),
		CustomerID:    "cust_" + uuid.NewString()[:8],
		CustomerEmail: email,
		Status:        domain.ScheduleActive,
		IntervalDays:  30,
		AnchorDate:    domain.NewDate(2026, time.January, 1),
		Timezone:      "UTC",
	}
	if err := repo.CreateSchedule(context.Background(), s, nil); err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	return s
}

// The core contract: an event with no notification yet gets one, computed from the
// schedule's anchor rather than a possibly-unmaterialized next_run_date.
func TestDispatcherSendsForUnnotifiedEvent(t *testing.T) {
	db := testsupport.DB(t)
	repo := store.New(db)
	sender := &fakeSender{failFor: map[string]bool{}}
	d := &notify.Dispatcher{Repo: repo, Sender: sender, SupportEmail: "support@cadenceos.example"}

	s := newSchedule(t, repo, "customer@example.com")
	if err := repo.AppendEvent(context.Background(), domain.ScheduleEvent{
		ScheduleID: s.ID, EventType: domain.EventScheduleCreated, Actor: domain.ActorCustomer,
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	res, err := d.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.Sent != 1 || res.Failed != 0 {
		t.Fatalf("got %+v, want 1 sent, 0 failed", res)
	}

	msg, ok := sender.sentTo("customer@example.com")
	if !ok {
		t.Fatal("no message sent to the schedule's customer_email")
	}
	// anchor 2026-01-01 + 30 days = 2026-01-31, occurrence 1.
	if want := "2026-01-31"; msg.HTMLBody == "" {
		t.Error("empty HTML body")
	} else if !strings.Contains(msg.HTMLBody, want) {
		t.Errorf("body %q does not mention the computed next order date %q", msg.HTMLBody, want)
	}
}

// The idempotency guarantee the whole outbox design exists for: a second run must
// not resend anything already recorded.
func TestDispatcherIsIdempotent(t *testing.T) {
	db := testsupport.DB(t)
	repo := store.New(db)
	sender := &fakeSender{failFor: map[string]bool{}}
	d := &notify.Dispatcher{Repo: repo, Sender: sender, SupportEmail: "support@cadenceos.example"}

	s := newSchedule(t, repo, "customer2@example.com")
	if err := repo.AppendEvent(context.Background(), domain.ScheduleEvent{
		ScheduleID: s.ID, EventType: domain.EventScheduleCreated, Actor: domain.ActorCustomer,
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	if _, err := d.RunOnce(context.Background()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	res, err := d.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if res.Sent != 0 {
		t.Errorf("second run sent %d, want 0 — the event was already notified", res.Sent)
	}

	sender.mu.Lock()
	count := 0
	for _, m := range sender.sent {
		if m.To == "customer2@example.com" {
			count++
		}
	}
	sender.mu.Unlock()
	if count != 1 {
		t.Errorf("customer2@example.com received %d sends, want exactly 1", count)
	}
}

// One failing send must not stop the batch — the rest of the events are still
// processed, and the failed one is left unnotified for the next run to retry.
func TestDispatcherOneFailureDoesNotStopTheBatch(t *testing.T) {
	db := testsupport.DB(t)
	repo := store.New(db)
	sender := &fakeSender{failFor: map[string]bool{"broken@example.com": true}}
	d := &notify.Dispatcher{Repo: repo, Sender: sender, SupportEmail: "support@cadenceos.example"}

	broken := newSchedule(t, repo, "broken@example.com")
	good := newSchedule(t, repo, "good@example.com")
	for _, s := range []domain.Schedule{broken, good} {
		if err := repo.AppendEvent(context.Background(), domain.ScheduleEvent{
			ScheduleID: s.ID, EventType: domain.EventScheduleCreated, Actor: domain.ActorCustomer,
		}); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}

	res, err := d.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.Sent != 1 || res.Failed != 1 {
		t.Fatalf("got %+v, want 1 sent and 1 failed", res)
	}
	if _, ok := sender.sentTo("good@example.com"); !ok {
		t.Error("the good schedule's email was not sent despite the other one failing")
	}

	// The failed one must remain in the unnotified set for the next run.
	pending, err := repo.UnnotifiedEvents(context.Background(), notify.NotifiableEventTypes())
	if err != nil {
		t.Fatalf("UnnotifiedEvents: %v", err)
	}
	var stillPending bool
	for _, e := range pending {
		if e.ScheduleID == broken.ID {
			stillPending = true
		}
	}
	if !stillPending {
		t.Error("the failed send was recorded as notified anyway — it will never be retried")
	}
}

// Only the four spec §7 sends this phase covers trigger anything -- an event type
// outside that set (skip, defer, cadence change) must produce no notification.
func TestDispatcherIgnoresOutOfScopeEventTypes(t *testing.T) {
	db := testsupport.DB(t)
	repo := store.New(db)
	sender := &fakeSender{failFor: map[string]bool{}}
	d := &notify.Dispatcher{Repo: repo, Sender: sender, SupportEmail: "support@cadenceos.example"}

	s := newSchedule(t, repo, "customer3@example.com")
	if err := repo.AppendEvent(context.Background(), domain.ScheduleEvent{
		ScheduleID: s.ID, EventType: domain.EventScheduleCadenceChanged, Actor: domain.ActorCustomer,
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	res, err := d.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.Sent != 0 {
		t.Errorf("sent %d for an out-of-scope event type, want 0", res.Sent)
	}
}
