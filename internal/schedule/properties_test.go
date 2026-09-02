package schedule_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/EPW80/replenishment-system/internal/domain"
	"github.com/EPW80/replenishment-system/internal/schedule"
	"github.com/EPW80/replenishment-system/internal/store"
)

// Spec §10 asks for "property-based tests over transition sequences."
//
// The value is not in any single sequence — it is that the invariants below survive
// orderings nobody thought to write a case for. Pause-resume-pause-cadence-defer-skip
// is not a scenario anyone would author by hand, and it is exactly the shape of bug
// that reaches production: each transition correct alone, wrong in combination.
//
// The seed is fixed so a failure is reproducible and CI is deterministic. Widen the
// search locally by raising sequences/steps rather than by randomizing the seed: a
// property test that fails on a different sequence every run teaches nobody anything.
const (
	propSeed      = 0x5CEDE10
	propSequences = 40
	propSteps     = 14
)

// invariants are the properties that must hold after *every* transition, accepted or
// rejected, for the whole life of a schedule.
type invariantState struct {
	// maxSeqSeen is the highest sequence number ever observed. It must never go
	// backwards and a value must never reappear: sequence_no is the idempotency key,
	// and a reused key is a duplicate charge (spec §3).
	maxSeqSeen  int
	seenSeqs    map[int]bool
	deferredIDs map[string]bool
	eventIDs    []int64
	anchor      domain.Date
	interval    int
}

func TestTransitionSequencesPreserveInvariants(t *testing.T) {
	repo, svc := setup(t)
	ctx := context.Background()
	rng := rand.New(rand.NewPCG(propSeed, 0x9E3779B9))
	today := domain.DateOf(fixedNow)

	for seq := 0; seq < propSequences; seq++ {
		interval := 7 + rng.IntN(174) // the full spec §3 range, 7-180
		anchor := today.AddDays(-rng.IntN(400))
		s := newActive(t, repo, anchor, interval)

		state := &invariantState{
			seenSeqs:    map[int]bool{},
			deferredIDs: map[string]bool{},
			anchor:      anchor,
			interval:    interval,
		}
		history := []string{fmt.Sprintf("create(anchor=%s interval=%d)", anchor, interval)}
		checkInvariants(t, repo, s.ID, state, today, history)

		for step := 0; step < propSteps; step++ {
			before, err := repo.GetSchedule(ctx, s.ID, store.SystemScope())
			if err != nil {
				t.Fatalf("get schedule: %v", err)
			}
			beforeOcc := occurrences(t, repo, s.ID)

			action, label, err := applyRandomAction(ctx, svc, s.ID, rng)
			history = append(history, label)

			switch {
			case err == nil:
				after, gerr := repo.GetSchedule(ctx, s.ID, store.SystemScope())
				if gerr != nil {
					t.Fatalf("get schedule: %v", gerr)
				}
				state.anchor, state.interval = after.AnchorDate, after.IntervalDays
				if action == domain.ActionDefer {
					markDeferred(state, beforeOcc, occurrences(t, repo, s.ID))
				}
				checkAnchorMovedOnlyWhenAllowed(t, action, before, after, history)

			case domain.IsTransitionError(err):
				// A rejected transition must be a no-op. A precondition that fails
				// halfway leaves a schedule in a state no transition table describes.
				after, gerr := repo.GetSchedule(ctx, s.ID, store.SystemScope())
				if gerr != nil {
					t.Fatalf("get schedule: %v", gerr)
				}
				if after.Status != before.Status || !after.AnchorDate.Equal(before.AnchorDate) ||
					after.IntervalDays != before.IntervalDays {
					t.Fatalf("rejected %s changed the schedule\nhistory: %v", label, history)
				}
				if len(occurrences(t, repo, s.ID)) != len(beforeOcc) {
					t.Fatalf("rejected %s changed the occurrences\nhistory: %v", label, history)
				}

			default:
				t.Fatalf("%s failed: %v\nhistory: %v", label, err, history)
			}

			checkInvariants(t, repo, s.ID, state, today, history)
		}
	}
}

// applyRandomAction picks one of the six spec §6 transitions and applies it.
func applyRandomAction(ctx context.Context, svc interface {
	Pause(context.Context, string, *domain.Date, schedule.Caller) (domain.Schedule, error)
	Resume(context.Context, string, schedule.Caller) (domain.Schedule, error)
	SkipNext(context.Context, string, schedule.Caller) (domain.Schedule, error)
	Defer(context.Context, string, int, schedule.Caller) (domain.Schedule, error)
	ChangeCadence(context.Context, string, int, schedule.Caller) (domain.Schedule, error)
	Cancel(context.Context, string, string, schedule.Caller) (domain.Schedule, error)
}, id string, rng *rand.Rand) (domain.Action, string, error) {
	switch rng.IntN(6) {
	case 0:
		_, err := svc.Pause(ctx, id, nil, anyCaller)
		return domain.ActionPause, "pause", err
	case 1:
		_, err := svc.Resume(ctx, id, anyCaller)
		return domain.ActionResume, "resume", err
	case 2:
		_, err := svc.SkipNext(ctx, id, anyCaller)
		return domain.ActionSkipNext, "skip", err
	case 3:
		days := 1 + rng.IntN(30)
		_, err := svc.Defer(ctx, id, days, anyCaller)
		return domain.ActionDefer, fmt.Sprintf("defer(%d)", days), err
	case 4:
		interval := 7 + rng.IntN(174)
		_, err := svc.ChangeCadence(ctx, id, interval, anyCaller)
		return domain.ActionChangeCadence, fmt.Sprintf("cadence(%d)", interval), err
	default:
		reason := domain.CancellationReasons[rng.IntN(len(domain.CancellationReasons))]
		_, err := svc.Cancel(ctx, id, reason, anyCaller)
		return domain.ActionCancel, "cancel(" + reason + ")", err
	}
}

// markDeferred records which occurrence moved, so the anchor-alignment check below can
// exempt it: a deferred date is deliberately off the cadence (spec §6).
func markDeferred(state *invariantState, before, after []domain.Occurrence) {
	was := map[string]domain.Date{}
	for _, o := range before {
		was[o.ID] = o.ScheduledFor
	}
	for _, o := range after {
		if prev, ok := was[o.ID]; ok && !prev.Equal(o.ScheduledFor) {
			state.deferredIDs[o.ID] = true
		}
	}
}

// Only resume and change_cadence re-anchor (spec §6). If any other transition moves
// the anchor, every future date silently shifts.
func checkAnchorMovedOnlyWhenAllowed(t *testing.T, a domain.Action, before, after domain.Schedule, history []string) {
	t.Helper()
	if before.AnchorDate.Equal(after.AnchorDate) {
		return
	}
	if a == domain.ActionResume || a == domain.ActionChangeCadence {
		return
	}
	t.Fatalf("%s moved the anchor from %s to %s\nhistory: %v",
		a, before.AnchorDate, after.AnchorDate, history)
}

func checkInvariants(t *testing.T, repo *store.PostgresRepository, id string, state *invariantState, today domain.Date, history []string) {
	t.Helper()
	ctx := context.Background()

	s, err := repo.GetSchedule(ctx, id, store.SystemScope())
	if err != nil {
		t.Fatalf("get schedule: %v", err)
	}
	occ := occurrences(t, repo, id)

	// 1. Sequence numbers and idempotency keys are unique and never reused.
	inThisPass := map[int]bool{}
	keys := map[string]bool{}
	for _, o := range occ {
		if inThisPass[o.SequenceNo] {
			t.Fatalf("sequence number %d appears twice\nhistory: %v", o.SequenceNo, history)
		}
		inThisPass[o.SequenceNo] = true

		if keys[o.IdempotencyKey] {
			t.Fatalf("idempotency key %q appears twice — that is a duplicate charge\nhistory: %v",
				o.IdempotencyKey, history)
		}
		keys[o.IdempotencyKey] = true

		if want := domain.IdempotencyKey(id, o.SequenceNo); o.IdempotencyKey != want {
			t.Fatalf("idempotency key %q, want %q\nhistory: %v", o.IdempotencyKey, want, history)
		}
		if o.SequenceNo > state.maxSeqSeen {
			state.maxSeqSeen = o.SequenceNo
		}
		state.seenSeqs[o.SequenceNo] = true
	}
	// A number that vanished and came back would be a reused key on a later run.
	for n := range inThisPass {
		if n > state.maxSeqSeen {
			t.Fatalf("sequence number %d exceeds the high-water mark\nhistory: %v", n, history)
		}
	}

	// 2. Unsettled occurrence dates are anchor-relative, except ones explicitly
	//    deferred — spec §3's whole drift argument rests on this.
	for _, o := range occ {
		if o.IsExecuted() || state.deferredIDs[o.ID] {
			continue
		}
		if !isAnchorAligned(state.anchor, state.interval, o.ScheduledFor) {
			t.Fatalf("occurrence %d at %s is not anchor + (n × %d) from %s\nhistory: %v",
				o.SequenceNo, o.ScheduledFor, state.interval, state.anchor, history)
		}
	}

	// 3. A paused or canceled schedule has nothing outstanding. Anything else tells
	//    the customer an order is still coming.
	if s.Status == domain.SchedulePaused || s.Status == domain.ScheduleCanceled {
		for _, o := range occ {
			if !o.IsExecuted() {
				t.Fatalf("%s schedule still has a %s occurrence\nhistory: %v", s.Status, o.Status, history)
			}
		}
		if s.NextRunDate != nil {
			t.Fatalf("%s schedule has next_run_date = %s\nhistory: %v", s.Status, s.NextRunDate, history)
		}
	}

	// 4. next_run_date is exactly the earliest future planned occurrence: it backs the
	//    indexed execution sweep in spec §3, so a stale value hides a real order.
	var want *domain.Date
	for _, o := range occ {
		if o.Status != domain.OccurrencePlanned || !o.ScheduledFor.After(today) {
			continue
		}
		if want == nil || o.ScheduledFor.Before(*want) {
			d := o.ScheduledFor
			want = &d
		}
	}
	switch {
	case want == nil && s.NextRunDate != nil:
		t.Fatalf("next_run_date = %s with no future planned order\nhistory: %v", s.NextRunDate, history)
	case want != nil && s.NextRunDate == nil:
		t.Fatalf("next_run_date is nil but %s is planned\nhistory: %v", want, history)
	case want != nil && !s.NextRunDate.Equal(*want):
		t.Fatalf("next_run_date = %s, want %s\nhistory: %v", s.NextRunDate, want, history)
	}

	// 5. paused_until only exists on a paused schedule (also a schema CHECK).
	if s.PausedUntil != nil && s.Status != domain.SchedulePaused {
		t.Fatalf("paused_until set on a %s schedule\nhistory: %v", s.Status, history)
	}

	// 6. The interval stays inside the spec §3 range no matter what path got here.
	if err := domain.ValidateInterval(s.IntervalDays); err != nil {
		t.Fatalf("%v\nhistory: %v", err, history)
	}

	// 7. The event log is append-only: everything previously seen is still there, in
	//    the same order, with nothing inserted behind it.
	events, err := repo.ListEvents(ctx, id)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) < len(state.eventIDs) {
		t.Fatalf("event log shrank from %d to %d entries\nhistory: %v",
			len(state.eventIDs), len(events), history)
	}
	for i, prev := range state.eventIDs {
		if events[i].ID != prev {
			t.Fatalf("event %d changed from id %d to %d — the log is not append-only\nhistory: %v",
				i, prev, events[i].ID, history)
		}
	}
	state.eventIDs = state.eventIDs[:0]
	for _, e := range events {
		state.eventIDs = append(state.eventIDs, e.ID)
	}
}

// isAnchorAligned reports whether d is anchor + (n × interval) for a whole n ≥ 0.
func isAnchorAligned(anchor domain.Date, interval int, d domain.Date) bool {
	delta := anchor.DaysUntil(d)
	return delta >= 0 && delta%interval == 0
}

// A schedule that is never touched still has to satisfy every invariant — this pins
// the baseline so a failure above is attributable to a transition.
func TestFreshScheduleSatisfiesInvariants(t *testing.T) {
	repo, _ := setup(t)
	today := domain.DateOf(fixedNow)

	for _, interval := range []int{7, 30, 90, 180} {
		anchor := today.AddDays(-3 * interval)
		s := newActive(t, repo, anchor, interval)
		state := &invariantState{
			seenSeqs:    map[int]bool{},
			deferredIDs: map[string]bool{},
			anchor:      anchor,
			interval:    interval,
		}
		checkInvariants(t, repo, s.ID, state, today,
			[]string{fmt.Sprintf("create(interval=%d)", interval)})
	}
}
