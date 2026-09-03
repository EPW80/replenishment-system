package domain

import (
	"errors"
	"fmt"
)

// Action is one of the six customer- or admin-initiated transitions in spec §6.
//
// The string values are the spec's own names for them, so an event payload, an API
// path and this constant all read the same way.
type Action string

const (
	ActionPause         Action = "pause"
	ActionResume        Action = "resume"
	ActionSkipNext      Action = "skip_next"
	ActionDefer         Action = "defer"
	ActionChangeCadence Action = "change_cadence"
	ActionCancel        Action = "cancel"
)

// MaxDeferDays is the absolute cap on a single deferral, set to the longest cadence
// the service supports.
//
// An unbounded defer is a cancellation with extra steps: it would let a customer push
// a shipment past the point where the schedule still means anything, while leaving it
// "active" in every report and every forecast. This is a blunt ceiling rather than a
// per-schedule limit — bounding a defer by the schedule's own interval would be
// tighter, and is worth revisiting once there is data on how customers use it, but
// that needs the interval passed in and this package stays pure.
const MaxDeferDays = MaxIntervalDays

// TransitionError reports an action attempted against a schedule that cannot accept
// it. It is a precondition failure, not a malformed request — the HTTP layer maps it
// to 409, not 400.
type TransitionError struct {
	Action  Action
	Status  ScheduleStatus
	Message string
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("cannot %s a %s schedule: %s", e.Action, e.Status, e.Message)
}

// CustomerMessage is the text safe to return to a customer.
//
// Spec §2's copy rule governs every one of these: they say when the next *order* is
// placed and what state the *schedule* is in. None of them refers to the product being
// used, needed, or run out of.
func (e *TransitionError) CustomerMessage() string { return e.Message }

// IsTransitionError reports whether err is a precondition failure.
func IsTransitionError(err error) bool {
	var te *TransitionError
	return errors.As(err, &te)
}

// scheduleTransitions is the precondition column of the spec §6 table, as data rather
// than as a chain of if-statements. Every action names exactly the statuses it accepts;
// anything absent is rejected, so a status added later fails closed.
var scheduleTransitions = map[Action][]ScheduleStatus{
	ActionPause:         {ScheduleActive},
	ActionResume:        {SchedulePaused},
	ActionSkipNext:      {ScheduleActive},
	ActionDefer:         {ScheduleActive},
	ActionChangeCadence: {ScheduleActive, SchedulePaused},
	// "any non-canceled" (spec §6). A failed schedule is a recoverable asset (§7), so
	// it stays cancelable — that is how a customer ends one after dunning gives up.
	ActionCancel: {ScheduleActive, SchedulePaused, ScheduleFailed},
}

// transitionMessages explains a rejection in commerce terms.
var transitionMessages = map[Action]map[ScheduleStatus]string{
	ActionPause: {
		SchedulePaused:   "this schedule is already paused",
		ScheduleCanceled: "this schedule has been canceled",
		ScheduleFailed:   "this schedule needs a payment method update before it can be paused",
	},
	ActionResume: {
		ScheduleActive:   "this schedule is already active",
		ScheduleCanceled: "this schedule has been canceled and cannot be resumed",
		ScheduleFailed:   "this schedule needs a payment method update before it can be resumed",
	},
	ActionSkipNext: {
		SchedulePaused:   "this schedule is paused, so no order is scheduled to skip",
		ScheduleCanceled: "this schedule has been canceled",
		ScheduleFailed:   "this schedule has no upcoming order to skip",
	},
	ActionDefer: {
		SchedulePaused:   "this schedule is paused, so no order is scheduled to move",
		ScheduleCanceled: "this schedule has been canceled",
		ScheduleFailed:   "this schedule has no upcoming order to move",
	},
	ActionChangeCadence: {
		ScheduleCanceled: "this schedule has been canceled",
		ScheduleFailed:   "this schedule needs a payment method update before its cadence can change",
	},
	ActionCancel: {
		ScheduleCanceled: "this schedule has already been canceled",
	},
}

// ValidateScheduleTransition checks an action against a schedule's status (spec §6).
func ValidateScheduleTransition(status ScheduleStatus, a Action) error {
	allowed, known := scheduleTransitions[a]
	if !known {
		return fmt.Errorf("unknown action %q", a)
	}
	for _, s := range allowed {
		if s == status {
			return nil
		}
	}

	msg := "this schedule cannot accept that change right now"
	if m, ok := transitionMessages[a][status]; ok {
		msg = m
	}
	return &TransitionError{Action: a, Status: status, Message: msg}
}

// ValidateOccurrenceAction checks an action against the occurrence it targets.
//
// skip_next and defer both act on a specific upcoming order, so the schedule's status
// is only half the precondition: an occurrence already placed, skipped, failed or
// canceled is settled and must not be rewritten. Reuses IsExecuted so there is one
// definition of "settled".
func ValidateOccurrenceAction(o Occurrence, a Action) error {
	if a != ActionSkipNext && a != ActionDefer {
		return fmt.Errorf("action %q does not target an occurrence", a)
	}
	if !o.IsExecuted() {
		return nil
	}

	msg := "this order has already been placed and can no longer be changed"
	switch o.Status {
	case OccurrenceSkipped:
		msg = "this order has already been skipped"
	case OccurrenceCanceled:
		msg = "this order has already been canceled"
	case OccurrenceFailed:
		msg = "this order could not be placed; update your payment method to restart the schedule"
	}
	return &TransitionError{Action: a, Status: ScheduleActive, Message: msg}
}

// ValidateDeferDays bounds a deferral (see MaxDeferDays).
func ValidateDeferDays(days int) error {
	if days < 1 || days > MaxDeferDays {
		return fmt.Errorf("days must be between 1 and %d, got %d", MaxDeferDays, days)
	}
	return nil
}

// Cancellation reason codes.
//
// Spec §8 projects churn analysis off these, and free text does not aggregate — a
// thousand distinct spellings of "too expensive" answer no question. Kept deliberately
// short and commercial: why the customer stopped *ordering*, never anything about the
// product's use.
const (
	ReasonTooExpensive   = "too_expensive"
	ReasonTooFrequent    = "too_frequent"
	ReasonSwitchedBrand  = "switched_brand"
	ReasonDeliveryIssue  = "delivery_issue"
	ReasonPaymentIssue   = "payment_issue"
	ReasonNoLongerWanted = "no_longer_wanted"
	ReasonOther          = "other"
)

// CancellationReasons is the closed set accepted by the cancel action.
var CancellationReasons = []string{
	ReasonTooExpensive,
	ReasonTooFrequent,
	ReasonSwitchedBrand,
	ReasonDeliveryIssue,
	ReasonPaymentIssue,
	ReasonNoLongerWanted,
	ReasonOther,
}

// ValidateCancellationReason checks a reason code against the closed set.
// MaxIdempotencyKeyLength bounds the client-supplied retry key on SkipNext and
// Defer. Generous enough for a UUID or any reasonable client-generated token, tight
// enough that the column can't become a place to stash arbitrary data.
const MaxIdempotencyKeyLength = 200

// ValidateIdempotencyKey checks the retry key SkipNext and Defer require.
//
// Empty is rejected rather than treated as "no key": a caller that forgot to generate
// one is a bug in the caller, and the two occurrences it can silently combine (spec
// context, see docs/adr/0009) are not a case to paper over with a default.
func ValidateIdempotencyKey(key string) error {
	if key == "" {
		return fmt.Errorf("idempotency_key is required")
	}
	if len(key) > MaxIdempotencyKeyLength {
		return fmt.Errorf("idempotency_key must be at most %d characters, got %d", MaxIdempotencyKeyLength, len(key))
	}
	return nil
}

func ValidateCancellationReason(code string) error {
	for _, r := range CancellationReasons {
		if r == code {
			return nil
		}
	}
	return fmt.Errorf("reason_code must be one of %v, got %q", CancellationReasons, code)
}
