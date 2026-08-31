// Command materialize tops up the planned-occurrence horizon for every active
// schedule (spec §5 step 1).
//
// Intended to run nightly. It is idempotent, so running it twice — or concurrently
// with a retry — creates nothing the second time and cannot produce a duplicate
// occurrence.
//
// It places no orders. Arm, Execute and Reconcile (spec §5 steps 2-4) are Phase 2.
package main

import (
	"context"
	"database/sql"
	"flag"
	"log/slog"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/EPW80/replenishment-system/internal/config"
	"github.com/EPW80/replenishment-system/internal/domain"
	"github.com/EPW80/replenishment-system/internal/materialize"
	"github.com/EPW80/replenishment-system/internal/store"
)

func main() {
	today := flag.String("today", "", "run as if today were this date (YYYY-MM-DD); defaults to the current UTC date")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := config.Load()
	if err != nil {
		log.Error("load config", "error", err)
		os.Exit(1)
	}

	run := domain.DateOf(time.Now().UTC())
	if *today != "" {
		parsed, err := time.Parse("2006-01-02", *today)
		if err != nil {
			log.Error("invalid -today", "value", *today, "error", err)
			os.Exit(1)
		}
		run = domain.DateOf(parsed)
	}

	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		log.Error("open database", "error", err)
		os.Exit(1)
	}
	defer func() { _ = db.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	res, err := materialize.New(store.New(db), cfg.MaterializeHorizon, log).RunAll(ctx, run)

	// Always report what was done, including on a partial failure: RunAll continues
	// past a single bad schedule so one customer cannot stall everyone else's.
	log.Info("materialization complete",
		"date", run.String(),
		"horizon", cfg.MaterializeHorizon,
		"schedules_considered", res.SchedulesConsidered,
		"occurrences_created", res.OccurrencesCreated,
		"duplicates_skipped", res.DuplicatesSkipped)

	if err != nil {
		log.Error("materialization finished with errors", "error", err)
		os.Exit(1)
	}
}
