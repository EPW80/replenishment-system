package schedule_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EPW80/replenishment-system/internal/domain"
	"github.com/EPW80/replenishment-system/internal/materialize"
	"github.com/EPW80/replenishment-system/internal/schedule"
	"github.com/EPW80/replenishment-system/internal/store"
	"github.com/EPW80/replenishment-system/internal/testsupport"
)

// anyCaller is an unscoped caller, for the tests below that are about transition
// behaviour rather than about who may perform it. Ownership scoping has its own tests.
var anyCaller = schedule.Caller{Actor: domain.ActorCustomer, Scope: store.SystemScope()}

// The clock is fixed so every expected date in this file is arithmetic, not a
// function of when the suite runs.
var fixedNow = time.Date(2026, time.January, 15, 12, 0, 0, 0, time.UTC)

const horizon = 3

func setup(t *testing.T) (*store.PostgresRepository, *schedule.Service) {
	t.Helper()
	repo := store.New(testsupport.DB(t))
	mat := materialize.New(repo, horizon, nil)
	return repo, schedule.New(repo, mat, func() time.Time { return fixedNow })
}

// newActive creates an active schedule with a materialized horizon, which is the state
// every transition below starts from.
func newActive(t *testing.T, repo *store.PostgresRepository, anchor domain.Date, interval int) domain.Schedule {
	t.Helper()
	ctx := context.Background()

	s := domain.Schedule{
		ID:           uuid.NewString(),
		CustomerID:   "cust_" + uuid.NewString()[:8],
		Status:       domain.ScheduleActive,
		IntervalDays: interval,
		AnchorDate:   anchor,
		Timezone:     "UTC",
	}
	if err := repo.CreateSchedule(ctx, s, []domain.ScheduleItem{
		{ID: uuid.NewString(), ScheduleID: s.ID, SKU: "SKU-001", Quantity: 1},
	}); err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	if _, _, err := materialize.New(repo, horizon, nil).Run(ctx, s, domain.DateOf(fixedNow)); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	return s
}

func occurrences(t *testing.T, repo *store.PostgresRepository, id string) []domain.Occurrence {
	t.Helper()
	out, err := repo.ListOccurrences(context.Background(), id, store.SystemScope())
	if err != nil {
		t.Fatalf("list occurrences: %v", err)
	}
	return out
}

func byStatus(occ []domain.Occurrence, st domain.OccurrenceStatus) []domain.Occurrence {
	var out []domain.Occurrence
	for _, o := range occ {
		if o.Status == st {
			out = append(out, o)
		}
	}
	return out
}

func lastEvent(t *testing.T, repo *store.PostgresRepository, id string) domain.ScheduleEvent {
	t.Helper()
	events, err := repo.ListEvents(context.Background(), id)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("no events recorded")
	}
	return events[len(events)-1]
}

// ---------------------------------------------------------------- pause / resume

// A paused schedule must show no upcoming orders. Leaving them planned would tell the
// customer they are still going to be charged.
func TestPauseCancelsUpcomingOrders(t *testing.T) {
	repo, svc := setup(t)
	ctx := context.Background()
	s := newActive(t, repo, domain.NewDate(2026, time.January, 1), 30)

	got, err := svc.Pause(ctx, s.ID, nil, anyCaller)
	if err != nil {
		t.Fatalf("pause: %v", err)
	}
	if got.Status != domain.SchedulePaused {
		t.Errorf("status = %s, want paused", got.Status)
	}
	if got.NextRunDate != nil {
		t.Errorf("next_run_date = %s on a paused schedule, want nil", got.NextRunDate)
	}
	if n := len(byStatus(occurrences(t, repo, s.ID), domain.OccurrenceCanceled)); n != horizon {
		t.Errorf("canceled %d occurrences, want %d", n, horizon)
	}

	ev := lastEvent(t, repo, s.ID)
	if ev.EventType != domain.EventSchedulePaused || ev.Actor != domain.ActorCustomer {
		t.Errorf("event = %s by %s, want %s by customer", ev.EventType, ev.Actor, domain.EventSchedulePaused)
	}
}

func TestPauseStoresPausedUntil(t *testing.T) {
	repo, svc := setup(t)
	s := newActive(t, repo, domain.NewDate(2026, time.January, 1), 30)

	until := domain.NewDate(2026, time.March, 1)
	got, err := svc.Pause(context.Background(), s.ID, &until, anyCaller)
	if err != nil {
		t.Fatalf("pause: %v", err)
	}
	if got.PausedUntil == nil || !got.PausedUntil.Equal(until) {
		t.Errorf("paused_until = %v, want %s", got.PausedUntil, until)
	}
}

func TestPauseRejectsPastResumeDate(t *testing.T) {
	repo, svc := setup(t)
	s := newActive(t, repo, domain.NewDate(2026, time.January, 1), 30)

	past := domain.NewDate(2025, time.December, 1)
	_, err := svc.Pause(context.Background(), s.ID, &past, anyCaller)
	if !domain.IsTransitionError(err) {
		t.Fatalf("err = %v, want a TransitionError for a past resume date", err)
	}
}

func TestPauseIsNotRepeatable(t *testing.T) {
	repo, svc := setup(t)
	ctx := context.Background()
	s := newActive(t, repo, domain.NewDate(2026, time.January, 1), 30)

	if _, err := svc.Pause(ctx, s.ID, nil, anyCaller); err != nil {
		t.Fatalf("first pause: %v", err)
	}
	if _, err := svc.Pause(ctx, s.ID, nil, anyCaller); !domain.IsTransitionError(err) {
		t.Fatalf("second pause returned %v, want a TransitionError", err)
	}
}

// Spec §6: resume re-anchors to today. A schedule paused for months must not come back
// and immediately charge for the shipments it missed.
func TestResumeReAnchorsToToday(t *testing.T) {
	repo, svc := setup(t)
	ctx := context.Background()
	s := newActive(t, repo, domain.NewDate(2025, time.June, 1), 30)

	if _, err := svc.Pause(ctx, s.ID, nil, anyCaller); err != nil {
		t.Fatalf("pause: %v", err)
	}
	got, err := svc.Resume(ctx, s.ID, anyCaller)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}

	today := domain.DateOf(fixedNow)
	if got.Status != domain.ScheduleActive {
		t.Errorf("status = %s, want active", got.Status)
	}
	if !got.AnchorDate.Equal(today) {
		t.Errorf("anchor = %s, want today (%s)", got.AnchorDate, today)
	}
	if got.PausedUntil != nil {
		t.Errorf("paused_until = %s on a resumed schedule, want nil", got.PausedUntil)
	}

	// The next order is one interval out, not one interval from the old anchor.
	want := today.AddDays(30)
	if got.NextRunDate == nil || !got.NextRunDate.Equal(want) {
		t.Fatalf("next_run_date = %v, want %s", got.NextRunDate, want)
	}
	if n := len(byStatus(occurrences(t, repo, s.ID), domain.OccurrencePlanned)); n != horizon {
		t.Errorf("planned %d occurrences after resume, want the horizon of %d", n, horizon)
	}
}

// Sequence numbers must survive a pause/resume cycle without restarting.
func TestResumeDoesNotReuseSequenceNumbers(t *testing.T) {
	repo, svc := setup(t)
	ctx := context.Background()
	s := newActive(t, repo, domain.NewDate(2026, time.January, 1), 30)

	before := occurrences(t, repo, s.ID)
	if _, err := svc.Pause(ctx, s.ID, nil, anyCaller); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if _, err := svc.Resume(ctx, s.ID, anyCaller); err != nil {
		t.Fatalf("resume: %v", err)
	}

	seen := map[int]bool{}
	for _, o := range before {
		seen[o.SequenceNo] = true
	}
	for _, o := range byStatus(occurrences(t, repo, s.ID), domain.OccurrencePlanned) {
		if seen[o.SequenceNo] {
			t.Errorf("sequence number %d reused after resume — that is a reused idempotency key", o.SequenceNo)
		}
	}
}

// ------------------------------------------------------------------ skip / defer

// Spec §6: skipping one order leaves every later one on its original date. That only
// holds because dates derive from the anchor rather than from the order before them.
func TestSkipNextLeavesLaterOrdersOnTheirDates(t *testing.T) {
	repo, svc := setup(t)
	ctx := context.Background()
	s := newActive(t, repo, domain.NewDate(2026, time.January, 1), 30)

	before := occurrences(t, repo, s.ID)
	skipped := before[0]
	survivors := map[int]domain.Date{}
	for _, o := range before[1:] {
		survivors[o.SequenceNo] = o.ScheduledFor
	}

	if _, err := svc.SkipNext(ctx, s.ID, anyCaller); err != nil {
		t.Fatalf("skip: %v", err)
	}

	for _, o := range occurrences(t, repo, s.ID) {
		if o.SequenceNo == skipped.SequenceNo {
			if o.Status != domain.OccurrenceSkipped {
				t.Errorf("targeted occurrence status = %s, want skipped", o.Status)
			}
			continue
		}
		if want, ok := survivors[o.SequenceNo]; ok && !o.ScheduledFor.Equal(want) {
			t.Errorf("occurrence %d moved from %s to %s — a skip must not slide the schedule",
				o.SequenceNo, want, o.ScheduledFor)
		}
	}

	// The horizon is topped back up so the portal still shows a full upcoming queue.
	after, err := repo.GetSchedule(ctx, s.ID, store.SystemScope())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.NextRunDate == nil || !after.NextRunDate.Equal(survivors[before[1].SequenceNo]) {
		t.Errorf("next_run_date = %v, want the order after the skipped one (%s)",
			after.NextRunDate, before[1].ScheduledFor)
	}
	if n, _ := repo.CountFutureplannedOccurrences(ctx, s.ID, domain.DateOf(fixedNow)); n != horizon {
		t.Errorf("future planned = %d after skip, want the horizon of %d", n, horizon)
	}
}

// Spec §6: defer shifts the occurrence and not the anchor, so the customer returns to
// their normal rhythm afterward rather than permanently sliding.
func TestDeferMovesOnlyTheNextOrder(t *testing.T) {
	repo, svc := setup(t)
	ctx := context.Background()
	s := newActive(t, repo, domain.NewDate(2026, time.January, 1), 30)

	before := occurrences(t, repo, s.ID)
	target, following := before[0], before[1]

	got, err := svc.Defer(ctx, s.ID, 7, anyCaller)
	if err != nil {
		t.Fatalf("defer: %v", err)
	}
	if !got.AnchorDate.Equal(s.AnchorDate) {
		t.Errorf("anchor moved from %s to %s — defer must not re-anchor", s.AnchorDate, got.AnchorDate)
	}

	for _, o := range occurrences(t, repo, s.ID) {
		switch o.SequenceNo {
		case target.SequenceNo:
			if want := target.ScheduledFor.AddDays(7); !o.ScheduledFor.Equal(want) {
				t.Errorf("deferred order at %s, want %s", o.ScheduledFor, want)
			}
		case following.SequenceNo:
			if !o.ScheduledFor.Equal(following.ScheduledFor) {
				t.Errorf("the order after the deferred one moved to %s, want %s — the "+
					"customer should return to their normal rhythm", o.ScheduledFor, following.ScheduledFor)
			}
		}
	}
}

func TestDeferRejectsOutOfRangeDays(t *testing.T) {
	repo, svc := setup(t)
	s := newActive(t, repo, domain.NewDate(2026, time.January, 1), 30)

	for _, days := range []int{0, -3, domain.MaxDeferDays + 1} {
		if _, err := svc.Defer(context.Background(), s.ID, days, anyCaller); err == nil {
			t.Errorf("defer of %d days was accepted", days)
		}
	}
}

func TestSkipAndDeferRejectedOnPausedSchedule(t *testing.T) {
	repo, svc := setup(t)
	ctx := context.Background()
	s := newActive(t, repo, domain.NewDate(2026, time.January, 1), 30)
	if _, err := svc.Pause(ctx, s.ID, nil, anyCaller); err != nil {
		t.Fatalf("pause: %v", err)
	}

	if _, err := svc.SkipNext(ctx, s.ID, anyCaller); !domain.IsTransitionError(err) {
		t.Errorf("skip on a paused schedule returned %v, want a TransitionError", err)
	}
	if _, err := svc.Defer(ctx, s.ID, 7, anyCaller); !domain.IsTransitionError(err) {
		t.Errorf("defer on a paused schedule returned %v, want a TransitionError", err)
	}
}

// ---------------------------------------------------------------- change cadence

// Spec §6: a cadence change re-anchors to the last placed order, so the new interval
// measures from the customer's most recent shipment.
func TestChangeCadenceReAnchorsToLastPlacedOrder(t *testing.T) {
	repo, svc := setup(t)
	ctx := context.Background()
	s := newActive(t, repo, domain.NewDate(2026, time.January, 1), 30)

	placed := occurrences(t, repo, s.ID)[0]
	if err := repo.UpdateOccurrenceStatus(ctx, placed.ID, domain.OccurrencePlaced); err != nil {
		t.Fatalf("mark placed: %v", err)
	}

	got, err := svc.ChangeCadence(ctx, s.ID, 60, anyCaller)
	if err != nil {
		t.Fatalf("change cadence: %v", err)
	}
	if got.IntervalDays != 60 {
		t.Errorf("interval_days = %d, want 60", got.IntervalDays)
	}
	if !got.AnchorDate.Equal(placed.ScheduledFor) {
		t.Errorf("anchor = %s, want the last placed order's date (%s)", got.AnchorDate, placed.ScheduledFor)
	}

	for i, o := range byStatus(occurrences(t, repo, s.ID), domain.OccurrencePlanned) {
		want := placed.ScheduledFor.AddDays(60 * (i + 1))
		if !o.ScheduledFor.Equal(want) {
			t.Errorf("planned occurrence %d at %s, want %s", i+1, o.ScheduledFor, want)
		}
	}
}

// With nothing shipped yet there is no last order to measure from, so today is the
// only honest anchor.
func TestChangeCadenceAnchorsToTodayWhenNothingPlaced(t *testing.T) {
	repo, svc := setup(t)
	s := newActive(t, repo, domain.NewDate(2026, time.January, 1), 30)

	got, err := svc.ChangeCadence(context.Background(), s.ID, 45, anyCaller)
	if err != nil {
		t.Fatalf("change cadence: %v", err)
	}
	if today := domain.DateOf(fixedNow); !got.AnchorDate.Equal(today) {
		t.Errorf("anchor = %s, want today (%s)", got.AnchorDate, today)
	}
}

// Spec §5: a cadence change rewrites planned occurrences and leaves pending ones
// alone. A pending occurrence has already had its pre-billing notice sent, and moving
// it would contradict the notice the customer was just given.
func TestChangeCadenceLeavesPendingOccurrencesAlone(t *testing.T) {
	repo, svc := setup(t)
	ctx := context.Background()
	s := newActive(t, repo, domain.NewDate(2026, time.January, 1), 30)

	pending := occurrences(t, repo, s.ID)[0]
	if err := repo.UpdateOccurrenceStatus(ctx, pending.ID, domain.OccurrencePending); err != nil {
		t.Fatalf("arm occurrence: %v", err)
	}

	if _, err := svc.ChangeCadence(ctx, s.ID, 90, anyCaller); err != nil {
		t.Fatalf("change cadence: %v", err)
	}

	for _, o := range occurrences(t, repo, s.ID) {
		if o.SequenceNo != pending.SequenceNo {
			continue
		}
		if o.Status != domain.OccurrencePending {
			t.Errorf("pending occurrence status = %s, want it left pending", o.Status)
		}
		if !o.ScheduledFor.Equal(pending.ScheduledFor) {
			t.Errorf("pending occurrence moved from %s to %s", pending.ScheduledFor, o.ScheduledFor)
		}
	}
}

func TestChangeCadenceRejectsOutOfRangeInterval(t *testing.T) {
	repo, svc := setup(t)
	s := newActive(t, repo, domain.NewDate(2026, time.January, 1), 30)

	for _, interval := range []int{0, 6, 181, -30} {
		if _, err := svc.ChangeCadence(context.Background(), s.ID, interval, anyCaller); err == nil {
			t.Errorf("interval of %d days was accepted", interval)
		}
	}
}

// ------------------------------------------------------------------------ cancel

func TestCancelSettlesEverything(t *testing.T) {
	repo, svc := setup(t)
	ctx := context.Background()
	s := newActive(t, repo, domain.NewDate(2026, time.January, 1), 30)

	got, err := svc.Cancel(ctx, s.ID, domain.ReasonTooExpensive, anyCaller)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if got.Status != domain.ScheduleCanceled {
		t.Errorf("status = %s, want canceled", got.Status)
	}
	if got.NextRunDate != nil {
		t.Errorf("next_run_date = %s on a canceled schedule, want nil", got.NextRunDate)
	}
	for _, o := range occurrences(t, repo, s.ID) {
		if !o.IsExecuted() {
			t.Errorf("occurrence %d left in %s after cancel", o.SequenceNo, o.Status)
		}
	}

	// Spec §8 projects churn analysis off the reason code, so it has to be on the event.
	ev := lastEvent(t, repo, s.ID)
	if ev.EventType != domain.EventScheduleCanceled {
		t.Fatalf("event = %s, want %s", ev.EventType, domain.EventScheduleCanceled)
	}
	if ev.ReasonCode == nil || *ev.ReasonCode != domain.ReasonTooExpensive {
		t.Errorf("reason_code = %v, want %q", ev.ReasonCode, domain.ReasonTooExpensive)
	}
}

func TestCancelRequiresAKnownReason(t *testing.T) {
	repo, svc := setup(t)
	s := newActive(t, repo, domain.NewDate(2026, time.January, 1), 30)

	for _, reason := range []string{"", "changed my mind", "TOO_EXPENSIVE"} {
		if _, err := svc.Cancel(context.Background(), s.ID, reason, anyCaller); err == nil {
			t.Errorf("cancel accepted reason %q; the set is closed so churn analysis can aggregate", reason)
		}
	}
}

// A canceled schedule is terminal: spec §7 makes a *failed* schedule the recoverable
// one, not a canceled one.
func TestCanceledScheduleAcceptsNoFurtherTransitions(t *testing.T) {
	repo, svc := setup(t)
	ctx := context.Background()
	s := newActive(t, repo, domain.NewDate(2026, time.January, 1), 30)
	if _, err := svc.Cancel(ctx, s.ID, domain.ReasonOther, anyCaller); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	checks := map[string]error{
		"pause":   errOf(func() error { _, e := svc.Pause(ctx, s.ID, nil, anyCaller); return e }),
		"resume":  errOf(func() error { _, e := svc.Resume(ctx, s.ID, anyCaller); return e }),
		"skip":    errOf(func() error { _, e := svc.SkipNext(ctx, s.ID, anyCaller); return e }),
		"defer":   errOf(func() error { _, e := svc.Defer(ctx, s.ID, 7, anyCaller); return e }),
		"cadence": errOf(func() error { _, e := svc.ChangeCadence(ctx, s.ID, 60, anyCaller); return e }),
		"cancel":  errOf(func() error { _, e := svc.Cancel(ctx, s.ID, domain.ReasonOther, anyCaller); return e }),
	}
	for name, err := range checks {
		if !domain.IsTransitionError(err) {
			t.Errorf("%s on a canceled schedule returned %v, want a TransitionError", name, err)
		}
	}
}

func errOf(f func() error) error { return f() }

// ------------------------------------------------------------------------ general

func TestTransitionsOnMissingScheduleReportNotFound(t *testing.T) {
	_, svc := setup(t)
	ctx := context.Background()
	missing := uuid.NewString()

	if _, err := svc.Pause(ctx, missing, nil, anyCaller); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("pause on a missing schedule returned %v, want ErrNotFound", err)
	}
	if _, err := svc.Cancel(ctx, missing, domain.ReasonOther, anyCaller); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("cancel on a missing schedule returned %v, want ErrNotFound", err)
	}
}

// Every transition writes exactly one event describing it. The log is the source the
// spec §8 read models project from, so a silent transition is a hole in the data.
func TestEveryTransitionRecordsExactlyOneEvent(t *testing.T) {
	repo, svc := setup(t)
	ctx := context.Background()
	s := newActive(t, repo, domain.NewDate(2026, time.January, 1), 30)

	countTransitionEvents := func() int {
		events, err := repo.ListEvents(ctx, s.ID)
		if err != nil {
			t.Fatalf("list events: %v", err)
		}
		n := 0
		for _, e := range events {
			switch e.EventType {
			case domain.EventSchedulePaused, domain.EventScheduleResumed,
				domain.EventScheduleCadenceChanged, domain.EventScheduleCanceled,
				domain.EventOccurrenceSkipped, domain.EventOccurrenceDeferred:
				n++
			}
		}
		return n
	}

	steps := []struct {
		name string
		run  func() error
	}{
		{"skip", func() error { _, e := svc.SkipNext(ctx, s.ID, anyCaller); return e }},
		{"defer", func() error { _, e := svc.Defer(ctx, s.ID, 5, anyCaller); return e }},
		{"cadence", func() error { _, e := svc.ChangeCadence(ctx, s.ID, 45, anyCaller); return e }},
		{"pause", func() error { _, e := svc.Pause(ctx, s.ID, nil, anyCaller); return e }},
		{"resume", func() error { _, e := svc.Resume(ctx, s.ID, anyCaller); return e }},
		{"cancel", func() error { _, e := svc.Cancel(ctx, s.ID, domain.ReasonOther, anyCaller); return e }},
	}
	for i, step := range steps {
		if err := step.run(); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if got := countTransitionEvents(); got != i+1 {
			t.Fatalf("after %s: %d transition events, want %d", step.name, got, i+1)
		}
	}
}

// Event payloads have to be readable JSON, since the spec §8 read models parse them.
func TestEventPayloadsAreValidJSON(t *testing.T) {
	repo, svc := setup(t)
	ctx := context.Background()
	s := newActive(t, repo, domain.NewDate(2026, time.January, 1), 30)

	if _, err := svc.Defer(ctx, s.ID, 3, anyCaller); err != nil {
		t.Fatalf("defer: %v", err)
	}
	if _, err := svc.Cancel(ctx, s.ID, domain.ReasonDeliveryIssue, anyCaller); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	events, _ := repo.ListEvents(ctx, s.ID)
	for _, e := range events {
		var body map[string]any
		if err := json.Unmarshal(e.Payload, &body); err != nil {
			t.Errorf("event %s payload is not JSON: %v", e.EventType, err)
		}
	}
}
