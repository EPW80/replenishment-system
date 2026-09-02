package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

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

// The empty scope must deny even against a row whose customer_id is itself empty.
//
// `customer_id` is NOT NULL but nothing forbids a blank value, so comparing it against
// the empty string is a match rather than a denial — which would turn the one scope
// meant to be fail-closed into a key for exactly the rows nobody owns. Denying has to
// be a property of the predicate, not of the data happening not to contain a blank.
func TestEmptyCustomerScopeDeniesEvenAnEmptyCustomerRow(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()

	orphan := domain.Schedule{
		ID:           uuid.NewString(),
		CustomerID:   "",
		Status:       domain.ScheduleActive,
		IntervalDays: 30,
		AnchorDate:   domain.NewDate(2026, time.March, 15),
		Timezone:     "UTC",
	}
	if err := repo.CreateSchedule(ctx, orphan, []domain.ScheduleItem{
		{ID: uuid.NewString(), ScheduleID: orphan.ID, SKU: "SKU-001", Quantity: 1},
	}); err != nil {
		t.Fatalf("create schedule with an empty customer_id: %v", err)
	}

	empty := store.CustomerScope("")

	if _, err := repo.GetSchedule(ctx, orphan.ID, empty); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetSchedule err = %v, want ErrNotFound", err)
	}
	if items, err := repo.ListScheduleItems(ctx, orphan.ID, empty); err != nil {
		t.Errorf("ListScheduleItems: %v", err)
	} else if len(items) != 0 {
		t.Errorf("got %d items under the empty scope, want 0", len(items))
	}
	if occ, err := repo.ListOccurrences(ctx, orphan.ID, empty); err != nil {
		t.Errorf("ListOccurrences: %v", err)
	} else if len(occ) != 0 {
		t.Errorf("got %d occurrences under the empty scope, want 0", len(occ))
	}
}

// GetScheduleForUpdate refuses to run outside a transaction.
//
// A FOR UPDATE in its own implicit transaction releases the lock as the statement
// returns, so it would read as locking while guaranteeing nothing. Failing loudly is
// what stops a future caller from adopting that false assurance and dropping the
// transaction around a transition.
func TestGetScheduleForUpdateRequiresATransaction(t *testing.T) {
	_, repo := newTestDB(t)
	ctx := context.Background()

	s := newSchedule(t, repo, domain.NewDate(2026, time.March, 15), 30)

	if _, err := repo.GetScheduleForUpdate(ctx, s.ID, store.SystemScope()); !errors.Is(err, store.ErrNoTransaction) {
		t.Errorf("err = %v, want ErrNoTransaction outside a transaction", err)
	}

	// Inside one it behaves as a scoped read that happens to hold the row.
	err := repo.InTx(ctx, func(tx store.Repository) error {
		got, err := tx.GetScheduleForUpdate(ctx, s.ID, store.SystemScope())
		if err != nil {
			return err
		}
		if got.ID != s.ID {
			t.Errorf("id = %s, want %s", got.ID, s.ID)
		}
		// The scope applies here too, or the lock would be a way around ownership.
		if _, err := tx.GetScheduleForUpdate(ctx, s.ID, store.CustomerScope("someone-else")); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("another customer's scope: err = %v, want ErrNotFound", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("in tx: %v", err)
	}
}
