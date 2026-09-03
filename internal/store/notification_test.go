package store_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/EPW80/replenishment-system/internal/domain"
)

var notifiableTypes = []string{domain.EventScheduleCreated}

// A freshly-appended event with no notification_log row yet must be claimable.
func TestClaimNotifiableEventsFindsNewWork(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()

	s := newSchedule(t, repo, domain.NewDate(2026, time.January, 1), 30)
	if err := repo.AppendEvent(ctx, domain.ScheduleEvent{
		ScheduleID: s.ID, EventType: domain.EventScheduleCreated, Actor: domain.ActorCustomer,
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	got, err := repo.ClaimNotifiableEvents(ctx, notifiableTypes, time.Hour, 10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(got) != 1 || got[0].ScheduleID != s.ID {
		t.Fatalf("claimed %+v, want exactly the one event for %s", got, s.ID)
	}
}

// Once claimed, the same event must not be handed out again while the claim is still
// fresh -- this is what stops two overlapping cmd/notify runs from both sending it.
func TestClaimNotifiableEventsDoesNotReclaimFreshPendingWork(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()

	s := newSchedule(t, repo, domain.NewDate(2026, time.January, 1), 30)
	if err := repo.AppendEvent(ctx, domain.ScheduleEvent{
		ScheduleID: s.ID, EventType: domain.EventScheduleCreated, Actor: domain.ActorCustomer,
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	first, err := repo.ClaimNotifiableEvents(ctx, notifiableTypes, time.Hour, 10)
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim: got %+v, err %v", first, err)
	}

	second, err := repo.ClaimNotifiableEvents(ctx, notifiableTypes, time.Hour, 10)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if len(second) != 0 {
		t.Errorf("second claim returned %d events, want 0 -- a fresh pending claim must not be handed out twice", len(second))
	}
}

// A claim that never resolved (the process crashed before marking sent or failed)
// must eventually be reclaimable, once it is older than the visibility timeout --
// otherwise a single crash would silently stop that event from ever notifying.
func TestClaimNotifiableEventsReclaimsStalePendingWork(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()

	s := newSchedule(t, repo, domain.NewDate(2026, time.January, 1), 30)
	if err := repo.AppendEvent(ctx, domain.ScheduleEvent{
		ScheduleID: s.ID, EventType: domain.EventScheduleCreated, Actor: domain.ActorCustomer,
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	if _, err := repo.ClaimNotifiableEvents(ctx, notifiableTypes, time.Hour, 10); err != nil {
		t.Fatalf("first claim: %v", err)
	}

	// A zero timeout means "immediately stale" -- the same effect as time passing,
	// without an actual sleep.
	reclaimed, err := repo.ClaimNotifiableEvents(ctx, notifiableTypes, 0, 10)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if len(reclaimed) != 1 {
		t.Fatalf("reclaimed %d events, want 1", len(reclaimed))
	}
}

// Once sent, an event must never be claimed again, no matter how the timeout is set.
func TestClaimNotifiableEventsNeverReclaimsSentWork(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()

	s := newSchedule(t, repo, domain.NewDate(2026, time.January, 1), 30)
	if err := repo.AppendEvent(ctx, domain.ScheduleEvent{
		ScheduleID: s.ID, EventType: domain.EventScheduleCreated, Actor: domain.ActorCustomer,
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	claimed, err := repo.ClaimNotifiableEvents(ctx, notifiableTypes, time.Hour, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: got %+v, err %v", claimed, err)
	}
	if err := repo.MarkNotificationSent(ctx, claimed[0].ScheduleEventID, time.Now()); err != nil {
		t.Fatalf("mark sent: %v", err)
	}

	again, err := repo.ClaimNotifiableEvents(ctx, notifiableTypes, 0, 10)
	if err != nil {
		t.Fatalf("re-claim after sent: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("got %d events after a sent one, want 0", len(again))
	}
}

// Below the attempt cap, a failed send stays pending and is reclaimable once stale.
// At the cap, it becomes permanently failed and must never be reclaimed again --
// otherwise one bad address retries forever and crowds out real work.
func TestMarkNotificationFailedRespectsTheAttemptCap(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()

	s := newSchedule(t, repo, domain.NewDate(2026, time.January, 1), 30)
	if err := repo.AppendEvent(ctx, domain.ScheduleEvent{
		ScheduleID: s.ID, EventType: domain.EventScheduleCreated, Actor: domain.ActorCustomer,
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	const maxAttempts = 3
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		claimed, err := repo.ClaimNotifiableEvents(ctx, notifiableTypes, 0, 10)
		if err != nil {
			t.Fatalf("claim attempt %d: %v", attempt, err)
		}
		if len(claimed) != 1 {
			t.Fatalf("claim attempt %d: got %d events, want 1 (attempts so far: %d)", attempt, len(claimed), attempt)
		}
		if err := repo.MarkNotificationFailed(ctx, claimed[0].ScheduleEventID, "simulated failure", maxAttempts); err != nil {
			t.Fatalf("mark failed attempt %d: %v", attempt, err)
		}
	}

	// The cap was reached on the maxAttempts-th failure; a further claim, even with a
	// zero timeout, must not return it.
	final, err := repo.ClaimNotifiableEvents(ctx, notifiableTypes, 0, 10)
	if err != nil {
		t.Fatalf("claim after cap: %v", err)
	}
	if len(final) != 0 {
		t.Errorf("got %d events after reaching the attempt cap, want 0", len(final))
	}
}

// Two schedules generate two independent claims -- a limit narrows the batch, it does
// not silently drop or merge unrelated events.
func TestClaimNotifiableEventsRespectsLimit(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()

	for range 3 {
		s := newSchedule(t, repo, domain.NewDate(2026, time.January, 1), 30)
		if err := repo.AppendEvent(ctx, domain.ScheduleEvent{
			ScheduleID: s.ID, EventType: domain.EventScheduleCreated, Actor: domain.ActorCustomer,
		}); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}

	got, err := repo.ClaimNotifiableEvents(ctx, notifiableTypes, time.Hour, 2)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events, want exactly the limit of 2", len(got))
	}
}

// An event type outside the requested set must never be claimed -- the dispatcher for
// one notification type must not accidentally consume another's work.
func TestClaimNotifiableEventsFiltersByEventType(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()

	s := newSchedule(t, repo, domain.NewDate(2026, time.January, 1), 30)
	if err := repo.AppendEvent(ctx, domain.ScheduleEvent{
		ScheduleID: s.ID, EventType: domain.EventOccurrencePlanned, Actor: domain.ActorSystem,
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	got, err := repo.ClaimNotifiableEvents(ctx, notifiableTypes, time.Hour, 10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("claimed %+v for an event type not in the requested set", got)
	}
}

// The cancellation template needs the human-readable reason, which lives on the
// event itself, not in its JSON payload -- ClaimNotifiableEvents must carry it.
func TestClaimNotifiableEventsCarriesReasonCode(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()

	s := newSchedule(t, repo, domain.NewDate(2026, time.January, 1), 30)
	reason := domain.ReasonTooExpensive
	if err := repo.AppendEvent(ctx, domain.ScheduleEvent{
		ScheduleID: s.ID, EventType: domain.EventScheduleCanceled,
		Actor: domain.ActorCustomer, ReasonCode: &reason,
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	got, err := repo.ClaimNotifiableEvents(ctx, []string{domain.EventScheduleCanceled}, time.Hour, 10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if got[0].ReasonCode == nil || *got[0].ReasonCode != reason {
		t.Errorf("reason_code = %v, want %q", got[0].ReasonCode, reason)
	}
}

// The property that actually matters: concurrent cmd/notify runs racing the same
// batch of events must claim disjoint sets, never the same event twice. Without
// FOR UPDATE ... SKIP LOCKED this is exactly the kind of race that would send a
// customer the same "your schedule was created" email from two overlapping runs.
func TestConcurrentClaimsNeverOverlap(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()

	const events = 20
	for range events {
		s := newSchedule(t, repo, domain.NewDate(2026, time.January, 1), 30)
		if err := repo.AppendEvent(ctx, domain.ScheduleEvent{
			ScheduleID: s.ID, EventType: domain.EventScheduleCreated, Actor: domain.ActorCustomer,
		}); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}

	const runners = 5
	var (
		start sync.WaitGroup
		done  sync.WaitGroup
		mu    sync.Mutex
		seen  = map[int64]int{}
	)
	start.Add(1)
	for i := 0; i < runners; i++ {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			claimed, err := repo.ClaimNotifiableEvents(ctx, notifiableTypes, time.Hour, events)
			if err != nil {
				t.Errorf("claim: %v", err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, e := range claimed {
				seen[e.ScheduleEventID]++
			}
		}()
	}
	start.Done()
	done.Wait()

	if len(seen) != events {
		t.Fatalf("%d distinct events claimed across all runners, want %d", len(seen), events)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("event %d claimed %d times, want exactly 1", id, n)
		}
	}
}
