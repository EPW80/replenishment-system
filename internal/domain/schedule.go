package domain

import "time"

// ScheduleStatus mirrors the schedule_status enum in the schema (spec §3).
type ScheduleStatus string

const (
	ScheduleActive   ScheduleStatus = "active"
	SchedulePaused   ScheduleStatus = "paused"
	ScheduleCanceled ScheduleStatus = "canceled"
	ScheduleFailed   ScheduleStatus = "failed"
)

// OccurrenceStatus mirrors the occurrence_status enum in the schema (spec §3).
type OccurrenceStatus string

const (
	OccurrencePlanned  OccurrenceStatus = "planned"
	OccurrencePending  OccurrenceStatus = "pending"
	OccurrencePlaced   OccurrenceStatus = "placed"
	OccurrenceSkipped  OccurrenceStatus = "skipped"
	OccurrenceFailed   OccurrenceStatus = "failed"
	OccurrenceCanceled OccurrenceStatus = "canceled"
)

// EventActor mirrors the schedule_event_actor enum. Every state transition records
// who caused it, because churn analysis (spec §8) needs to distinguish a customer
// cancelling from a system failure.
type EventActor string

const (
	ActorCustomer EventActor = "customer"
	ActorAdmin    EventActor = "admin"
	ActorSystem   EventActor = "system"
)

// Schedule is a customer's recurring order plan (spec §3).
//
// Note what this struct does not carry: no usage rate, no quantity-on-hand, no
// projection of when the customer will run out. Cadence is interval_days and
// nothing else (spec §2).
type Schedule struct {
	ID         string
	CustomerID string // WooCommerce customer ID
	Status     ScheduleStatus

	// OriginOrderID is the WooCommerce order ID from the checkout that established
	// this subscription. UNIQUE: it is schedule creation's idempotency key, the same
	// role occurrences.idempotency_key plays for a single occurrence. Submitting the
	// same origin_order_id twice must return the existing schedule, never create a
	// second one.
	OriginOrderID string

	IntervalDays int

	// AnchorDate is the schedule origin. Occurrence dates derive from it, never
	// from the previous run — see OccurrenceDate.
	AnchorDate  Date
	NextRunDate *Date

	Timezone string // IANA, from the customer profile

	// PaymentTokenRef is an opaque gateway vault reference. Never card data.
	PaymentTokenRef   string
	ShippingAddressID string

	// DiscountPct is stored per schedule so grandfathering is possible when the
	// replenishment discount changes (spec §7).
	DiscountPct float64

	PausedUntil *Date

	CreatedAt time.Time
	UpdatedAt time.Time
}

// IsActive reports whether the schedule should be materialized and executed.
func (s Schedule) IsActive() bool { return s.Status == ScheduleActive }

// ScheduleItem is one SKU and quantity on a schedule.
type ScheduleItem struct {
	ID         string
	ScheduleID string
	SKU        string
	Quantity   int
	CreatedAt  time.Time
}

// Occurrence is a single planned or attempted fulfillment — the unit of work
// (spec §3). Materialized ahead of time so the customer portal can show a real
// upcoming queue and so skips and defers have something concrete to act on.
type Occurrence struct {
	ID           string
	ScheduleID   string
	SequenceNo   int
	ScheduledFor Date
	Status       OccurrenceStatus

	// OrderID is the WooCommerce order, nil until placed.
	OrderID *string

	// IdempotencyKey is schedule_id:sequence_no, UNIQUE in Postgres and passed to
	// the gateway. See IdempotencyKey.
	IdempotencyKey string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// IsExecuted reports whether the occurrence has reached a terminal state that a
// cadence change must not rewrite (spec §5).
func (o Occurrence) IsExecuted() bool {
	switch o.Status {
	case OccurrencePlaced, OccurrenceSkipped, OccurrenceFailed, OccurrenceCanceled:
		return true
	default:
		return false
	}
}

// ScheduleEvent is one entry in the append-only audit log (spec §3).
//
// The read models in spec §8 project off this table, and churn analysis needs the
// reason codes. Nothing updates or deletes an event.
type ScheduleEvent struct {
	ID         int64
	ScheduleID string
	EventType  string
	Actor      EventActor
	ReasonCode *string
	Payload    []byte // JSON
	CreatedAt  time.Time
}

// Event type constants. Kept as constants so a typo cannot silently create a new
// event type that the read models will never aggregate.
const (
	EventScheduleCreated      = "schedule.created"
	EventOccurrencePlanned    = "occurrence.planned"
	EventOccurrenceMaterialed = "occurrence.materialized"

	// Spec §6 transitions. Each carries the actor who caused it and, for a
	// cancellation, the reason code the churn analysis in spec §8 aggregates.
	EventSchedulePaused         = "schedule.paused"
	EventScheduleResumed        = "schedule.resumed"
	EventScheduleCadenceChanged = "schedule.cadence_changed"
	EventScheduleCanceled       = "schedule.canceled"
	EventOccurrenceSkipped      = "occurrence.skipped"
	EventOccurrenceDeferred     = "occurrence.deferred"
)
