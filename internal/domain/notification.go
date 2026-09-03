package domain

import "time"

// NotifiableEvent is a schedule_events row the notification dispatcher (issue #4) has
// claimed from the outbox: one of the four event types Phase 4 sends an email for,
// not yet sent (or a reclaimed stuck attempt), with the fields a template needs.
//
// It carries ScheduleID rather than a resolved domain.Schedule: the dispatcher fetches
// the schedule fresh at send time (docs/adr/0010), so a customer_email changed after
// the event was recorded is still honored.
type NotifiableEvent struct {
	ScheduleEventID int64
	ScheduleID      string
	EventType       string
	ReasonCode      *string // set only for schedule.canceled
	Payload         []byte  // JSON, same shape as ScheduleEvent.Payload
	CreatedAt       time.Time
}
