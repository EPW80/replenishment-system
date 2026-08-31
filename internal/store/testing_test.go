package store_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"

	"github.com/EPW80/replenishment-system/internal/domain"
	"github.com/EPW80/replenishment-system/internal/store"
	"github.com/EPW80/replenishment-system/internal/testsupport"
)

// newTestDB returns a migrated database in a schema private to this test.
func newTestDB(t *testing.T) (*sql.DB, *store.PostgresRepository) {
	t.Helper()
	db := testsupport.DB(t)
	return db, store.New(db)
}

// newSchedule inserts an active schedule and returns it.
func newSchedule(t *testing.T, repo *store.PostgresRepository, anchor domain.Date, intervalDays int) domain.Schedule {
	t.Helper()

	s := domain.Schedule{
		ID:                uuid.NewString(),
		CustomerID:        "cust_" + uuid.NewString()[:8],
		Status:            domain.ScheduleActive,
		IntervalDays:      intervalDays,
		AnchorDate:        anchor,
		Timezone:          "America/Los_Angeles",
		PaymentTokenRef:   "tok_" + uuid.NewString()[:8], // opaque vault ref, never card data
		ShippingAddressID: "addr_1",
		DiscountPct:       10,
	}
	items := []domain.ScheduleItem{
		{ID: uuid.NewString(), ScheduleID: s.ID, SKU: "SKU-001", Quantity: 1},
	}

	if err := repo.CreateSchedule(context.Background(), s, items); err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	return s
}

func occurrence(scheduleID string, seq int, date domain.Date) domain.Occurrence {
	return domain.Occurrence{
		ID:             uuid.NewString(),
		ScheduleID:     scheduleID,
		SequenceNo:     seq,
		ScheduledFor:   date,
		Status:         domain.OccurrencePlanned,
		IdempotencyKey: domain.IdempotencyKey(scheduleID, seq),
	}
}
