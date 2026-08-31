package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/EPW80/replenishment-system/internal/domain"
)

var allStatuses = []domain.ScheduleStatus{
	domain.ScheduleActive,
	domain.SchedulePaused,
	domain.ScheduleCanceled,
	domain.ScheduleFailed,
}

var allActions = []domain.Action{
	domain.ActionPause,
	domain.ActionResume,
	domain.ActionSkipNext,
	domain.ActionDefer,
	domain.ActionChangeCadence,
	domain.ActionCancel,
}

// Spec §10 asks for "every state-transition pair, including the invalid ones."
//
// The table is written out in full rather than derived from the implementation. A test
// that computes its expectation from the code under test proves only that the code
// agrees with itself; this one encodes the spec §6 precondition column independently,
// so a change to the rules has to be a deliberate edit here too.
func TestEveryStateTransitionPair(t *testing.T) {
	allowed := map[domain.Action]map[domain.ScheduleStatus]bool{
		domain.ActionPause: {
			domain.ScheduleActive: true,
		},
		domain.ActionResume: {
			domain.SchedulePaused: true,
		},
		domain.ActionSkipNext: {
			domain.ScheduleActive: true,
		},
		domain.ActionDefer: {
			domain.ScheduleActive: true,
		},
		domain.ActionChangeCadence: {
			domain.ScheduleActive: true,
			domain.SchedulePaused: true,
		},
		// "any non-canceled" (spec §6).
		domain.ActionCancel: {
			domain.ScheduleActive: true,
			domain.SchedulePaused: true,
			domain.ScheduleFailed: true,
		},
	}

	for _, a := range allActions {
		for _, status := range allStatuses {
			want := allowed[a][status]
			err := domain.ValidateScheduleTransition(status, a)

			switch {
			case want && err != nil:
				t.Errorf("%s on a %s schedule was rejected: %v", a, status, err)
			case !want && err == nil:
				t.Errorf("%s on a %s schedule was allowed; spec §6 forbids it", a, status)
			case !want && !domain.IsTransitionError(err):
				t.Errorf("%s on a %s schedule returned %T, want a *TransitionError so the "+
					"HTTP layer can answer 409", a, status, err)
			}
		}
	}
}

// A canceled schedule is terminal. Nothing revives it — reactivation after dunning
// failure is a `failed` schedule (spec §7), not a canceled one.
func TestCanceledScheduleAcceptsNothing(t *testing.T) {
	for _, a := range allActions {
		if err := domain.ValidateScheduleTransition(domain.ScheduleCanceled, a); err == nil {
			t.Errorf("%s was allowed on a canceled schedule", a)
		}
	}
}

func TestUnknownActionIsRejected(t *testing.T) {
	err := domain.ValidateScheduleTransition(domain.ScheduleActive, domain.Action("delete_everything"))
	if err == nil {
		t.Fatal("an unknown action was accepted")
	}
	// An unknown action is a programming error, not a customer-facing precondition
	// failure, so it must not surface as a 409.
	if domain.IsTransitionError(err) {
		t.Error("an unknown action produced a TransitionError; it should not map to 409")
	}
}

// Rejection messages reach the customer, so spec §2's copy rule applies: they say when
// the next order is placed and what state the schedule is in, never anything about
// using the product.
func TestRejectionMessagesFollowTheCopyRule(t *testing.T) {
	banned := []string{
		"take", "taking", "dose", "doses", "dosage", "supply", "run out", "running out",
		"remaining", "left", "intake", "consume", "consumption", "need it", "your body",
	}

	for _, a := range allActions {
		for _, status := range allStatuses {
			err := domain.ValidateScheduleTransition(status, a)
			var te *domain.TransitionError
			if !errors.As(err, &te) {
				continue
			}
			msg := strings.ToLower(te.CustomerMessage())
			if msg == "" {
				t.Errorf("%s/%s produced an empty customer message", a, status)
			}
			for _, w := range banned {
				if strings.Contains(msg, w) {
					t.Errorf("%s/%s message %q contains %q — spec §2 copy rule is "+
						"\"when to reorder,\" never \"when to take\"", a, status, msg, w)
				}
			}
		}
	}
}

// skip and defer act on a specific order, so an occurrence already settled must be
// refused even when the schedule itself is active.
func TestValidateOccurrenceAction(t *testing.T) {
	settled := []domain.OccurrenceStatus{
		domain.OccurrencePlaced,
		domain.OccurrenceSkipped,
		domain.OccurrenceFailed,
		domain.OccurrenceCanceled,
	}
	open := []domain.OccurrenceStatus{
		domain.OccurrencePlanned,
		domain.OccurrencePending,
	}

	for _, a := range []domain.Action{domain.ActionSkipNext, domain.ActionDefer} {
		for _, st := range open {
			if err := domain.ValidateOccurrenceAction(domain.Occurrence{Status: st}, a); err != nil {
				t.Errorf("%s on a %s occurrence was rejected: %v", a, st, err)
			}
		}
		for _, st := range settled {
			err := domain.ValidateOccurrenceAction(domain.Occurrence{Status: st}, a)
			if err == nil {
				t.Errorf("%s on a %s occurrence was allowed; that rewrites a settled order", a, st)
				continue
			}
			if !domain.IsTransitionError(err) {
				t.Errorf("%s/%s returned %T, want a *TransitionError", a, st, err)
			}
		}
	}
}

// Actions that do not target an occurrence must not be silently accepted by the
// occurrence check — that would be a precondition quietly passing on the wrong object.
func TestValidateOccurrenceActionRejectsNonOccurrenceActions(t *testing.T) {
	for _, a := range []domain.Action{
		domain.ActionPause, domain.ActionResume, domain.ActionChangeCadence, domain.ActionCancel,
	} {
		if err := domain.ValidateOccurrenceAction(domain.Occurrence{Status: domain.OccurrencePlanned}, a); err == nil {
			t.Errorf("%s was accepted as an occurrence-targeting action", a)
		}
	}
}

func TestValidateDeferDays(t *testing.T) {
	for _, days := range []int{1, 7, 30, domain.MaxDeferDays} {
		if err := domain.ValidateDeferDays(days); err != nil {
			t.Errorf("defer of %d days rejected: %v", days, err)
		}
	}
	// Zero and negative are nonsense; an unbounded defer is a cancellation wearing a
	// disguise, leaving a schedule "active" in every report while never shipping.
	for _, days := range []int{0, -1, -365, domain.MaxDeferDays + 1, 10000} {
		if err := domain.ValidateDeferDays(days); err == nil {
			t.Errorf("defer of %d days was accepted", days)
		}
	}
}

func TestValidateCancellationReason(t *testing.T) {
	for _, r := range domain.CancellationReasons {
		if err := domain.ValidateCancellationReason(r); err != nil {
			t.Errorf("reason %q rejected: %v", r, err)
		}
	}
	// Free text does not aggregate, and spec §8 projects churn analysis off these.
	for _, r := range []string{"", "because", "TOO_EXPENSIVE", "too expensive", "other "} {
		if err := domain.ValidateCancellationReason(r); err == nil {
			t.Errorf("reason %q was accepted; the set is closed", r)
		}
	}
}

// Reason codes are commercial: why the customer stopped ordering. None of them may
// encode how much product they have or how they use it (spec §2).
func TestCancellationReasonsStayCommercial(t *testing.T) {
	banned := []string{"dose", "supply", "remaining", "left", "intake", "usage", "on_hand", "stock"}
	for _, r := range domain.CancellationReasons {
		for _, w := range banned {
			if strings.Contains(r, w) {
				t.Errorf("reason code %q contains %q — that is a consumption signal, not a churn reason", r, w)
			}
		}
	}
}
