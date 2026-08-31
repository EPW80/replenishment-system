package domain

import (
	"testing"
	"time"
)

// Spec §10 priority coverage: anchor-date arithmetic across DST boundaries, leap
// days, and month-end.

func TestOccurrenceDate_AnchorRelative(t *testing.T) {
	anchor := NewDate(2026, time.January, 1)

	for _, tc := range []struct {
		seq  int
		want Date
	}{
		{1, NewDate(2026, time.January, 31)},
		{2, NewDate(2026, time.March, 2)},
		{3, NewDate(2026, time.April, 1)},
		{12, NewDate(2026, time.December, 27)},
	} {
		got, err := OccurrenceDate(anchor, 30, tc.seq)
		if err != nil {
			t.Fatalf("seq %d: %v", tc.seq, err)
		}
		if !got.Equal(tc.want) {
			t.Errorf("occurrence %d = %s, want %s", tc.seq, got, tc.want)
		}
	}
}

// The property that makes anchor-relative computation worth the trouble: every
// occurrence depends only on the anchor, so nothing that happens to earlier
// occurrences can move a later one.
func TestOccurrenceDate_DoesNotDriftAcrossIntervals(t *testing.T) {
	anchor := NewDate(2026, time.March, 15)
	const interval = 45

	for seq := 1; seq <= 40; seq++ {
		anchorRelative, err := OccurrenceDate(anchor, interval, seq)
		if err != nil {
			t.Fatalf("seq %d: %v", seq, err)
		}

		// The naive alternative: add one interval at a time from the anchor.
		incremental := anchor
		for i := 0; i < seq; i++ {
			incremental = incremental.AddDays(interval)
		}

		// In pure date space these agree. The test pins that they do, so a future
		// change to Date.AddDays that reintroduces instant arithmetic — and with it
		// DST drift — fails here.
		if !anchorRelative.Equal(incremental) {
			t.Fatalf("seq %d: anchor-relative %s != incremental %s", seq, anchorRelative, incremental)
		}
	}
}

// A DST transition must not move a shipment date. This is the bug that
// time.Add(24h * n) produces: the 25-hour day absorbs an hour and the date slips.
func TestOccurrenceDate_AcrossDSTBoundaries(t *testing.T) {
	for _, zone := range []string{
		"America/Los_Angeles", // spring forward Mar 8 2026, fall back Nov 1 2026
		"America/New_York",
		"Europe/London",
		"Australia/Sydney", // southern hemisphere: transitions in the opposite months
	} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Skipf("timezone database unavailable for %s: %v", zone, err)
		}

		t.Run(zone, func(t *testing.T) {
			// Anchor a week before the US spring-forward transition.
			anchor := NewDate(2026, time.March, 1)

			for seq := 1; seq <= 10; seq++ {
				got, err := OccurrenceDate(anchor, 7, seq)
				if err != nil {
					t.Fatalf("seq %d: %v", seq, err)
				}
				want := NewDate(2026, time.March, 1+7*seq)
				if !got.Equal(want) {
					t.Errorf("occurrence %d = %s, want %s", seq, got, want)
				}

				// Every occurrence must still resolve to a real instant in this zone,
				// including on a day whose local midnight does not exist.
				start := got.StartOfDayIn(loc)
				if start.IsZero() {
					t.Errorf("occurrence %d has no valid instant in %s", seq, zone)
				}
				if DateOf(start) != got && start.Hour() == 0 {
					t.Errorf("occurrence %d: StartOfDayIn moved the date to %s", seq, DateOf(start))
				}
			}
		})
	}
}

// The transition day itself, from both directions.
func TestDate_AddDaysAcrossDSTTransitionDay(t *testing.T) {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skip("timezone database unavailable")
	}

	for _, tc := range []struct {
		name       string
		from, want Date
	}{
		{"day before spring forward", NewDate(2026, time.March, 7), NewDate(2026, time.March, 8)},
		{"spring forward day", NewDate(2026, time.March, 8), NewDate(2026, time.March, 9)},
		{"day before fall back", NewDate(2026, time.November, 1), NewDate(2026, time.November, 2)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.from.AddDays(1); !got.Equal(tc.want) {
				t.Errorf("%s + 1 day = %s, want %s", tc.from, got, tc.want)
			}
			// And the resulting date is addressable in a DST-observing zone.
			if got := tc.want.StartOfDayIn(loc); got.IsZero() {
				t.Errorf("%s has no valid instant in America/Los_Angeles", tc.want)
			}
		})
	}
}

func TestOccurrenceDate_LeapDay(t *testing.T) {
	t.Run("anchored on a leap day", func(t *testing.T) {
		anchor := NewDate(2028, time.February, 29) // 2028 is a leap year

		// A leap-day anchor is legal and must not special-case. Occurrence 2 crosses
		// into 2029, which is not a leap year, so the anchor date itself does not
		// recur -- exactly why the anchor is a fixed origin rather than a
		// "same day each period" rule.
		for _, tc := range []struct {
			seq  int
			want Date
		}{
			{1, NewDate(2028, time.August, 27)},
			{2, NewDate(2029, time.February, 23)},
		} {
			got, err := OccurrenceDate(anchor, MaxIntervalDays, tc.seq)
			if err != nil {
				t.Fatalf("seq %d: %v", tc.seq, err)
			}
			if !got.Equal(tc.want) {
				t.Errorf("occurrence %d = %s, want %s", tc.seq, got, tc.want)
			}
		}
	})

	t.Run("landing on Feb 29 from a non-leap-year anchor", func(t *testing.T) {
		// 180 days from Sep 1 2027 lands on Feb 28 2028; the day after is the leap
		// day, which must be a valid schedule date like any other.
		anchor := NewDate(2027, time.September, 1)
		got, err := OccurrenceDate(anchor, MaxIntervalDays, 1)
		if err != nil {
			t.Fatalf("OccurrenceDate: %v", err)
		}
		if want := NewDate(2028, time.February, 28); !got.Equal(want) {
			t.Errorf("got %s, want %s", got, want)
		}
		if next := got.AddDays(1); !next.Equal(NewDate(2028, time.February, 29)) {
			t.Errorf("day after = %s, want 2028-02-29", next)
		}
	})

	t.Run("crossing Feb 29", func(t *testing.T) {
		anchor := NewDate(2028, time.February, 1)
		got, err := OccurrenceDate(anchor, 30, 1)
		if err != nil {
			t.Fatalf("OccurrenceDate: %v", err)
		}
		// Feb 2028 has 29 days: Feb 1 + 30 = Mar 2.
		if want := NewDate(2028, time.March, 2); !got.Equal(want) {
			t.Errorf("got %s, want %s", got, want)
		}
	})

	t.Run("century non-leap year", func(t *testing.T) {
		// 1900 was not a leap year; 2000 was. Go handles this, but the schedule math
		// must not have introduced its own calendar.
		anchor := NewDate(2100, time.February, 1)
		got, err := OccurrenceDate(anchor, 30, 1)
		if err != nil {
			t.Fatalf("OccurrenceDate: %v", err)
		}
		if want := NewDate(2100, time.March, 3); !got.Equal(want) {
			t.Errorf("got %s, want %s", got, want)
		}
	})
}

func TestOccurrenceDate_MonthEnd(t *testing.T) {
	for _, tc := range []struct {
		name     string
		anchor   Date
		interval int
		seq      int
		want     Date
	}{
		{"Jan 31 + 30 lands in March", NewDate(2026, time.January, 31), 30, 1, NewDate(2026, time.March, 2)},
		{"Aug 31 + 31", NewDate(2026, time.August, 31), 31, 1, NewDate(2026, time.October, 1)},
		{"Dec 31 crosses the year", NewDate(2026, time.December, 31), 7, 1, NewDate(2027, time.January, 7)},
		{"Feb 28 non-leap + 1 day", NewDate(2026, time.February, 28), 7, 1, NewDate(2026, time.March, 7)},
		{"month-end anchor over 12 intervals", NewDate(2026, time.January, 31), 30, 12, NewDate(2027, time.January, 26)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := OccurrenceDate(tc.anchor, tc.interval, tc.seq)
			if err != nil {
				t.Fatalf("OccurrenceDate: %v", err)
			}
			if !got.Equal(tc.want) {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestOccurrenceDate_Validation(t *testing.T) {
	anchor := NewDate(2026, time.January, 1)

	for _, tc := range []struct {
		name     string
		interval int
		seq      int
	}{
		{"interval below the spec minimum", 6, 1},
		{"interval above the spec maximum", 181, 1},
		{"zero interval", 0, 1},
		{"negative interval", -30, 1},
		{"sequence zero", 30, 0},
		{"negative sequence", 30, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := OccurrenceDate(anchor, tc.interval, tc.seq); err == nil {
				t.Errorf("expected an error for interval=%d seq=%d", tc.interval, tc.seq)
			}
		})
	}

	// The spec §3 boundaries themselves are valid.
	for _, interval := range []int{MinIntervalDays, MaxIntervalDays} {
		if _, err := OccurrenceDate(anchor, interval, 1); err != nil {
			t.Errorf("interval %d should be valid: %v", interval, err)
		}
	}
}

func TestNextOccurrenceAfter(t *testing.T) {
	anchor := NewDate(2026, time.January, 1)

	for _, tc := range []struct {
		name    string
		after   Date
		wantSeq int
		wantDay Date
	}{
		{"before the first occurrence", NewDate(2026, time.January, 5), 1, NewDate(2026, time.January, 31)},
		{"exactly on an occurrence date", NewDate(2026, time.January, 31), 2, NewDate(2026, time.March, 2)},
		{"just after an occurrence", NewDate(2026, time.February, 1), 2, NewDate(2026, time.March, 2)},
		{"far in the future", NewDate(2026, time.December, 1), 12, NewDate(2026, time.December, 27)},
		{"before the anchor", NewDate(2025, time.June, 1), 1, NewDate(2026, time.January, 31)},
		{"on the anchor itself", NewDate(2026, time.January, 1), 1, NewDate(2026, time.January, 31)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seq, date, err := NextOccurrenceAfter(anchor, 30, tc.after)
			if err != nil {
				t.Fatalf("NextOccurrenceAfter: %v", err)
			}
			if seq != tc.wantSeq || !date.Equal(tc.wantDay) {
				t.Errorf("got seq=%d date=%s, want seq=%d date=%s", seq, date, tc.wantSeq, tc.wantDay)
			}
			// The contract: strictly after.
			if !date.After(tc.after) {
				t.Errorf("returned %s, which is not strictly after %s", date, tc.after)
			}
		})
	}
}

// The key must be deterministic: the same occurrence always produces the same key,
// or it is not an idempotency key at all.
func TestIdempotencyKey(t *testing.T) {
	const id = "3f7a1c9e-5b2d-8a4f-6e0c-1b3d5a7f9e2c"

	if got, want := IdempotencyKey(id, 7), id+":7"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// Deterministic: the same occurrence must always produce the same key, or a
	// retry would present a new key to the gateway and charge twice.
	first, second := IdempotencyKey(id, 1), IdempotencyKey(id, 1)
	if first != second {
		t.Errorf("key is not deterministic: %q then %q", first, second)
	}
	if IdempotencyKey(id, 1) == IdempotencyKey(id, 2) {
		t.Error("different occurrences must not share a key")
	}
	if IdempotencyKey("a", 1) == IdempotencyKey("b", 1) {
		t.Error("different schedules must not share a key")
	}
}

func TestDateComparisons(t *testing.T) {
	a := NewDate(2026, time.March, 15)
	b := NewDate(2026, time.March, 16)
	c := NewDate(2027, time.January, 1)

	if !a.Before(b) || !b.After(a) || !a.Equal(NewDate(2026, time.March, 15)) {
		t.Error("basic comparisons wrong")
	}
	if !a.Before(c) || !c.After(a) {
		t.Error("year comparison wrong")
	}
	if a.Before(a) || a.After(a) {
		t.Error("a date must be neither before nor after itself")
	}
	if got := a.DaysUntil(b); got != 1 {
		t.Errorf("DaysUntil = %d, want 1", got)
	}
	if got := b.DaysUntil(a); got != -1 {
		t.Errorf("reverse DaysUntil = %d, want -1", got)
	}
	// Across a DST boundary the day count must still be exact.
	if got := NewDate(2026, time.March, 7).DaysUntil(NewDate(2026, time.March, 9)); got != 2 {
		t.Errorf("DaysUntil across spring forward = %d, want 2", got)
	}
}
