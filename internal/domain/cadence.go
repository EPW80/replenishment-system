// Package domain holds the entities and the cadence arithmetic.
//
// Everything here is pure: no database, no clock, no I/O. That is deliberate. The
// date math below is the part of this service most likely to be wrong in a way that
// only shows up months later, so it has to be testable without standing anything up.
package domain

import (
	"fmt"
	"time"
)

// Cadence limits from spec §3. Enforced here and by a CHECK constraint in the schema,
// because a value that reaches the database has already escaped the domain layer.
const (
	MinIntervalDays = 7
	MaxIntervalDays = 180
)

// Date is a calendar date in a customer's timezone.
//
// It is deliberately not a time.Time. A shipment is scheduled for a *day*, not an
// instant, and treating it as an instant is how DST bugs get in: adding 24-hour
// durations across a DST boundary silently shifts the date by one. Every operation
// below works in date space.
type Date struct {
	Year  int
	Month time.Month
	Day   int
}

// NewDate returns the calendar date for y/m/d, normalizing out-of-range values the
// way time.Date does (Jan 32 becomes Feb 1).
func NewDate(y int, m time.Month, d int) Date {
	t := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	return Date{Year: t.Year(), Month: t.Month(), Day: t.Day()}
}

// DateOf returns the calendar date t falls on, in t's own location.
func DateOf(t time.Time) Date {
	return Date{Year: t.Year(), Month: t.Month(), Day: t.Day()}
}

// AddDays returns the date n days after d. n may be negative.
//
// This is calendar arithmetic in UTC, which has no DST transitions, so adding days
// here cannot skip or repeat one. The customer's timezone matters when a date is
// turned back into an instant — see StartOfDayIn — not when days are counted.
func (d Date) AddDays(n int) Date {
	t := time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC).AddDate(0, 0, n)
	return Date{Year: t.Year(), Month: t.Month(), Day: t.Day()}
}

// Before reports whether d is earlier than other.
func (d Date) Before(other Date) bool {
	if d.Year != other.Year {
		return d.Year < other.Year
	}
	if d.Month != other.Month {
		return d.Month < other.Month
	}
	return d.Day < other.Day
}

// After reports whether d is later than other.
func (d Date) After(other Date) bool { return other.Before(d) }

// Equal reports whether d and other are the same calendar date.
func (d Date) Equal(other Date) bool { return d == other }

// DaysUntil returns the number of days from d to other; negative if other is earlier.
func (d Date) DaysUntil(other Date) int {
	a := time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC)
	b := time.Date(other.Year, other.Month, other.Day, 0, 0, 0, 0, time.UTC)
	return int(b.Sub(a).Hours() / 24)
}

// String renders the date as YYYY-MM-DD.
func (d Date) String() string { return fmt.Sprintf("%04d-%02d-%02d", d.Year, int(d.Month), d.Day) }

// StartOfDayIn returns the instant the date begins in loc.
//
// This is where the customer's timezone finally matters: it decides when "the 15th"
// starts. On a DST spring-forward day where local midnight does not exist, Go
// normalizes to the following valid instant, which is the behaviour we want — the
// occurrence executes at the start of that day as the customer experiences it.
func (d Date) StartOfDayIn(loc *time.Location) time.Time {
	return time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, loc)
}

// ToTime returns the date as a UTC midnight instant, for storage in a date column.
func (d Date) ToTime() time.Time {
	return time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC)
}

// ValidateInterval checks an interval against the spec §3 range.
func ValidateInterval(intervalDays int) error {
	if intervalDays < MinIntervalDays || intervalDays > MaxIntervalDays {
		return fmt.Errorf("interval_days must be between %d and %d, got %d",
			MinIntervalDays, MaxIntervalDays, intervalDays)
	}
	return nil
}

// OccurrenceDate returns the date of the nth occurrence of a schedule.
//
// This is the single most important function in the package, and the reason it is
// written this way is spec §3:
//
//	next_run_date is always recomputed from anchor_date + (n × interval_days),
//	never from last_run + interval. Incremental addition accumulates drift across
//	skips, deferrals, and retries; anchor-relative computation does not.
//
// sequenceNo is 1-based: occurrence 1 is the first shipment after the anchor. Every
// occurrence derives from the anchor alone, so no sequence of skips, deferrals or
// retries can move a later occurrence off its intended date.
func OccurrenceDate(anchor Date, intervalDays, sequenceNo int) (Date, error) {
	if err := ValidateInterval(intervalDays); err != nil {
		return Date{}, err
	}
	if sequenceNo < 1 {
		return Date{}, fmt.Errorf("sequence_no must be at least 1, got %d", sequenceNo)
	}
	return anchor.AddDays(intervalDays * sequenceNo), nil
}

// NextOccurrenceAfter returns the sequence number and date of the first occurrence
// falling strictly after the given date.
//
// Used when re-materializing a horizon: it answers "where should this schedule pick
// up from?" without walking every past occurrence one interval at a time.
func NextOccurrenceAfter(anchor Date, intervalDays int, after Date) (sequenceNo int, date Date, err error) {
	if err := ValidateInterval(intervalDays); err != nil {
		return 0, Date{}, err
	}

	elapsed := anchor.DaysUntil(after)
	if elapsed < 0 {
		// The anchor is in the future; the first occurrence is still occurrence 1.
		d, err := OccurrenceDate(anchor, intervalDays, 1)
		return 1, d, err
	}

	// Integer division floors, so this is the last occurrence at or before `after`.
	seq := elapsed/intervalDays + 1
	d, err := OccurrenceDate(anchor, intervalDays, seq)
	if err != nil {
		return 0, Date{}, err
	}
	// Land strictly after: step one more when the computed date is not past `after`.
	if !d.After(after) {
		seq++
		d, err = OccurrenceDate(anchor, intervalDays, seq)
		if err != nil {
			return 0, Date{}, err
		}
	}
	return seq, d, nil
}

// IdempotencyKey returns the key for one occurrence: schedule_id:sequence_no.
//
// Spec §3 calls this "the whole safety story for order creation." It is UNIQUE in
// Postgres and passed to the payment gateway, so a retry, a duplicate queue delivery
// or a redeploy mid-run resolves to the same key and cannot produce a second charge.
// It must stay deterministic — never derive it from a clock or a random source.
func IdempotencyKey(scheduleID string, sequenceNo int) string {
	return fmt.Sprintf("%s:%d", scheduleID, sequenceNo)
}
