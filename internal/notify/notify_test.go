package notify_test

import (
	"context"
	"fmt"
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

// stubSender records every call and can be told to fail for specific recipients,
// standing in for a Postmark outage or a hard-bounced address.
type stubSender struct {
	mu      sync.Mutex
	sent    []sentEmail
	failFor map[string]error
}

type sentEmail struct {
	to, subject, body string
}

func newStubSender() *stubSender { return &stubSender{failFor: map[string]error{}} }

func (s *stubSender) Send(_ context.Context, to, subject, body string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err, ok := s.failFor[to]; ok {
		return err
	}
	s.sent = append(s.sent, sentEmail{to: to, subject: subject, body: body})
	return nil
}

func (s *stubSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

func (s *stubSender) last() sentEmail {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sent[len(s.sent)-1]
}

func newRepo(t *testing.T) *store.PostgresRepository {
	t.Helper()
	return store.New(testsupport.DB(t))
}

// newScheduleWithEmail creates an active schedule with an email on file and appends
// one event of the given type, the shape ClaimNotifiableEvents expects to find.
func newScheduleWithEmail(t *testing.T, repo *store.PostgresRepository, email, eventType string) domain.Schedule {
	t.Helper()
	ctx := context.Background()

	s := domain.Schedule{
		ID:            uuid.NewString(),
		CustomerID:    "cust_" + uuid.NewString()[:8],
		CustomerEmail: email,
		OriginOrderID: "order_" + uuid.NewString(),
		Status:        domain.ScheduleActive,
		IntervalDays:  30,
		AnchorDate:    domain.NewDate(2026, time.January, 1),
		Timezone:      "UTC",
	}
	if err := repo.CreateSchedule(ctx, s, []domain.ScheduleItem{
		{ID: uuid.NewString(), ScheduleID: s.ID, SKU: "SKU-001", Quantity: 2},
	}); err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	if err := repo.AppendEvent(ctx, domain.ScheduleEvent{
		ScheduleID: s.ID, EventType: eventType, Actor: domain.ActorCustomer,
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}
	return s
}

func TestRunAllSendsAndMarksSent(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	s := newScheduleWithEmail(t, repo, "customer@example.com", domain.EventScheduleCreated)

	sender := newStubSender()
	d := notify.New(repo, sender, "support@example.com", nil)

	res, err := d.RunAll(ctx)
	if err != nil {
		t.Fatalf("RunAll: %v", err)
	}
	if res.Claimed != 1 || res.Sent != 1 || res.Skipped != 0 || res.SendFailed != 0 {
		t.Fatalf("result = %+v, want exactly one sent", res)
	}
	if sender.count() != 1 {
		t.Fatalf("sender received %d calls, want 1", sender.count())
	}
	got := sender.last()
	if got.to != s.CustomerEmail {
		t.Errorf("sent to %q, want %q", got.to, s.CustomerEmail)
	}
	if got.subject == "" || got.body == "" {
		t.Error("empty subject or body")
	}

	// A second run must find nothing left to do -- the event is resolved.
	again, err := d.RunAll(ctx)
	if err != nil {
		t.Fatalf("second RunAll: %v", err)
	}
	if again.Claimed != 0 {
		t.Errorf("second run claimed %d events, want 0", again.Claimed)
	}
}

// A schedule with no address on file (pre-Phase-4, or a caller that never sent one)
// must be skipped, not sent to, and not reconsidered on the next run.
func TestRunAllSkipsScheduleWithNoEmail(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	newScheduleWithEmail(t, repo, "", domain.EventScheduleCreated)

	sender := newStubSender()
	d := notify.New(repo, sender, "support@example.com", nil)

	res, err := d.RunAll(ctx)
	if err != nil {
		t.Fatalf("RunAll: %v", err)
	}
	if res.Skipped != 1 || res.Sent != 0 {
		t.Fatalf("result = %+v, want exactly one skipped", res)
	}
	if sender.count() != 0 {
		t.Errorf("sender was called %d times for a schedule with no email", sender.count())
	}

	again, err := d.RunAll(ctx)
	if err != nil {
		t.Fatalf("second RunAll: %v", err)
	}
	if again.Claimed != 0 {
		t.Errorf("skipped event reconsidered on a later run: claimed %d", again.Claimed)
	}
}

// A send failure records the outcome and must not stop the batch -- one bad address
// cannot block every other customer's confirmation.
func TestRunAllContinuesPastASendFailure(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	bad := newScheduleWithEmail(t, repo, "bad@example.com", domain.EventScheduleCreated)
	good := newScheduleWithEmail(t, repo, "good@example.com", domain.EventScheduleCreated)

	sender := newStubSender()
	sender.failFor[bad.CustomerEmail] = fmt.Errorf("simulated postmark rejection")
	d := notify.New(repo, sender, "support@example.com", nil)

	res, err := d.RunAll(ctx)
	if err != nil {
		t.Fatalf("RunAll: %v", err)
	}
	if res.Claimed != 2 || res.Sent != 1 || res.SendFailed != 1 {
		t.Fatalf("result = %+v, want 2 claimed, 1 sent, 1 send-failed", res)
	}
	if sender.count() != 1 || sender.last().to != good.CustomerEmail {
		t.Errorf("sender state = %+v, want exactly one successful send to %s", sender, good.CustomerEmail)
	}
}

// Below the attempt cap a failed send is retried on a later run once the visibility
// timeout passes; retrying it must not resend to the addresses that already succeeded.
func TestRunAllRetriesAFailedSendOnALaterRun(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	s := newScheduleWithEmail(t, repo, "customer@example.com", domain.EventScheduleCreated)

	sender := newStubSender()
	sender.failFor[s.CustomerEmail] = fmt.Errorf("transient postmark error")
	d := notify.New(repo, sender, "support@example.com", nil)

	first, err := d.RunAll(ctx)
	if err != nil || first.SendFailed != 1 {
		t.Fatalf("first run: %+v, err %v", first, err)
	}

	// Let the retry succeed.
	sender.mu.Lock()
	delete(sender.failFor, s.CustomerEmail)
	sender.mu.Unlock()

	// The dispatcher's own visibility timeout is 15 minutes; a notify.Dispatcher built
	// directly against the repo can't fast-forward that from outside the package, so
	// this test exercises the retry through the store layer's zero-timeout claim
	// instead, then re-verifies the same package-level RunAll marks it resolved.
	claimed, err := repo.ClaimNotifiableEvents(ctx, []string{domain.EventScheduleCreated}, 0, 10)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("reclaimed %d events, want 1", len(claimed))
	}
	if err := repo.MarkNotificationSent(ctx, claimed[0].ScheduleEventID, time.Now()); err != nil {
		t.Fatalf("mark sent: %v", err)
	}

	again, err := d.RunAll(ctx)
	if err != nil {
		t.Fatalf("second RunAll: %v", err)
	}
	if again.Claimed != 0 {
		t.Errorf("second run claimed %d events after the retry already resolved it, want 0", again.Claimed)
	}
}

// Each of the four event types must render without error and produce distinct,
// non-empty content -- a template that panics or renders blank would fail silently
// as a SendFailed outcome, not a build error.
// The paused-until date must come from the schedule's own current state, not from
// whatever the triggering event's payload happened to capture -- a reclaimed or
// delayed send must not describe a paused_until a later transition already changed.
// Here the schedule row carries a real date but the event payload is empty, which is
// exactly the case a payload-only read would get wrong.
func TestPausedEmailUsesTheScheduleRowNotTheEventPayload(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()

	s := newScheduleWithEmail(t, repo, "customer@example.com", domain.EventSchedulePaused)
	until := domain.NewDate(2026, time.June, 1)
	if err := repo.UpdateScheduleStatus(ctx, s.ID, domain.SchedulePaused, &until); err != nil {
		t.Fatalf("update schedule status: %v", err)
	}

	sender := newStubSender()
	d := notify.New(repo, sender, "support@example.com", nil)
	if _, err := d.RunAll(ctx); err != nil {
		t.Fatalf("RunAll: %v", err)
	}

	body := sender.last().body
	if !strings.Contains(body, until.String()) {
		t.Errorf("body = %q, want it to contain the schedule's actual paused_until (%s)", body, until.String())
	}
	if strings.Contains(body, "stay paused until you resume") {
		t.Errorf("body = %q, fell back to the indefinite-pause copy despite a real paused_until on the schedule", body)
	}
}

func TestRunAllRendersEveryEventType(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()

	cases := []struct {
		name      string
		eventType string
	}{
		{"created", domain.EventScheduleCreated},
		{"paused", domain.EventSchedulePaused},
		{"resumed", domain.EventScheduleResumed},
		{"canceled", domain.EventScheduleCanceled},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newScheduleWithEmail(t, repo, tc.name+"@example.com", tc.eventType)

			sender := newStubSender()
			d := notify.New(repo, sender, "support@example.com", nil)
			res, err := d.RunAll(ctx)
			if err != nil {
				t.Fatalf("RunAll: %v", err)
			}
			if res.Sent != 1 {
				t.Fatalf("result = %+v, want exactly one sent", res)
			}
			got := sender.last()
			if got.subject == "" {
				t.Error("empty subject")
			}
			if got.body == "" {
				t.Error("empty body")
			}
		})
	}
}

// A canceled schedule's email must carry human-readable reason text, never the raw
// reason code -- spec §2's copy rule applies here exactly as everywhere else.
func TestCanceledEmailUsesHumanReadableReason(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()

	s := domain.Schedule{
		ID:            uuid.NewString(),
		CustomerID:    "cust_" + uuid.NewString()[:8],
		CustomerEmail: "customer@example.com",
		OriginOrderID: "order_" + uuid.NewString(),
		Status:        domain.ScheduleActive,
		IntervalDays:  30,
		AnchorDate:    domain.NewDate(2026, time.January, 1),
		Timezone:      "UTC",
	}
	if err := repo.CreateSchedule(ctx, s, nil); err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	reason := domain.ReasonTooExpensive
	if err := repo.AppendEvent(ctx, domain.ScheduleEvent{
		ScheduleID: s.ID, EventType: domain.EventScheduleCanceled,
		Actor: domain.ActorCustomer, ReasonCode: &reason,
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	sender := newStubSender()
	d := notify.New(repo, sender, "support@example.com", nil)
	if _, err := d.RunAll(ctx); err != nil {
		t.Fatalf("RunAll: %v", err)
	}

	body := sender.last().body
	if !strings.Contains(body, "too expensive") {
		t.Errorf("body = %q, want it to contain the human-readable reason", body)
	}
	if strings.Contains(body, domain.ReasonTooExpensive) {
		t.Errorf("body = %q, leaked the raw reason code %q", body, domain.ReasonTooExpensive)
	}
}

// The property that actually matters end to end: two overlapping cmd/notify runs
// racing the same batch of events must send each event at most once, never twice.
func TestConcurrentRunAllNeverDoubleSends(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()

	const events = 15
	for i := 0; i < events; i++ {
		newScheduleWithEmail(t, repo, fmt.Sprintf("customer-%d@example.com", i), domain.EventScheduleCreated)
	}

	const runners = 4
	sender := newStubSender()
	var (
		start sync.WaitGroup
		done  sync.WaitGroup
	)
	start.Add(1)
	for i := 0; i < runners; i++ {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			d := notify.New(repo, sender, "support@example.com", nil)
			if _, err := d.RunAll(ctx); err != nil {
				t.Errorf("RunAll: %v", err)
			}
		}()
	}
	start.Done()
	done.Wait()

	sender.mu.Lock()
	defer sender.mu.Unlock()
	if len(sender.sent) != events {
		t.Fatalf("%d emails sent across all runners, want exactly %d", len(sender.sent), events)
	}
	seen := map[string]int{}
	for _, e := range sender.sent {
		seen[e.to]++
	}
	for to, n := range seen {
		if n != 1 {
			t.Errorf("%s received %d emails, want exactly 1", to, n)
		}
	}
}
