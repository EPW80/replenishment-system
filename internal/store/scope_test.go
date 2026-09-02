package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EPW80/replenishment-system/internal/domain"
	"github.com/EPW80/replenishment-system/internal/store"
)

// The scope is the layer that makes cross-customer access impossible in SQL rather
// than dependent on a handler remembering to compare two IDs. These tests hold that
// property at the query layer, where it cannot be bypassed by a new caller.
func TestScopeLimitsReadsToOneCustomer(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()

	anchor := domain.NewDate(2026, time.March, 15)
	mine := newSchedule(t, repo, anchor, 30)
	theirs := newSchedule(t, repo, anchor, 30)
	if mine.CustomerID == theirs.CustomerID {
		t.Fatal("fixture produced one customer; the test would prove nothing")
	}

	if err := repo.CreateOccurrence(ctx, occurrence(theirs.ID, 1, anchor.AddDays(30))); err != nil {
		t.Fatalf("create occurrence: %v", err)
	}

	own := store.CustomerScope(mine.CustomerID)

	t.Run("GetSchedule", func(t *testing.T) {
		if _, err := repo.GetSchedule(ctx, mine.ID, own); err != nil {
			t.Errorf("own schedule: %v", err)
		}
		// ErrNotFound, not a distinct "forbidden" error: the handlers turn this into
		// a 404, and anything else would confirm the ID names a real schedule.
		if _, err := repo.GetSchedule(ctx, theirs.ID, own); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("another customer's schedule: err = %v, want ErrNotFound", err)
		}
	})

	t.Run("ListOccurrences", func(t *testing.T) {
		got, err := repo.ListOccurrences(ctx, theirs.ID, own)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("got %d occurrences from another customer's schedule, want 0", len(got))
		}
	})

	t.Run("ListScheduleItems", func(t *testing.T) {
		got, err := repo.ListScheduleItems(ctx, theirs.ID, own)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("got %d items from another customer's schedule, want 0", len(got))
		}
		if mineItems, err := repo.ListScheduleItems(ctx, mine.ID, own); err != nil {
			t.Errorf("own items: %v", err)
		} else if len(mineItems) == 0 {
			t.Error("own items came back empty; the scope is filtering too much")
		}
	})
}

// SystemScope is what the nightly materializer runs under, so it must still see
// everything — the scope limits callers, not the service's own background work.
func TestSystemScopeSeesEveryCustomer(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()

	anchor := domain.NewDate(2026, time.March, 15)
	a := newSchedule(t, repo, anchor, 30)
	b := newSchedule(t, repo, anchor, 30)

	for _, s := range []domain.Schedule{a, b} {
		if _, err := repo.GetSchedule(ctx, s.ID, store.SystemScope()); err != nil {
			t.Errorf("system scope could not read %s: %v", s.ID, err)
		}
	}
}

// A customer scope built from an empty ID must deny rather than widen. This is the
// failure mode that reads as a missing value and behaves as a privilege escalation:
// if the empty string meant "no filter", a request that lost its customer ID somewhere
// would quietly return every schedule in the database.
func TestEmptyCustomerScopeDeniesRatherThanWidens(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()

	s := newSchedule(t, repo, domain.NewDate(2026, time.March, 15), 30)

	if _, err := repo.GetSchedule(ctx, s.ID, store.CustomerScope("")); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound — an empty scope must match nothing", err)
	}
}
