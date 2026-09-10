// Command sweep runs the periodic passes that move schedules forward without a
// customer or an operator asking.
//
// Two passes today: ending timed pauses that have come due (spec §6 resume), and
// opening the pre-billing window on occurrences that have come within ArmWindowDays
// of their date (spec §5 step 2). Reconcile (spec §5 step 4) joins them once there is
// an execution path to reconcile against.
//
// Both run even if the first fails, and this exits non-zero if either did — the same
// policy scripts/nightly.sh applies to sweep and materialize, for the same reason: the
// two passes are independent, so one failing must not silently skip the other.
//
// Intended to run nightly, after materialize. Both passes are idempotent — a schedule
// already resumed, or an occurrence already armed, is not touched twice, so running
// this again, or concurrently with a retry, changes nothing.
//
// It places no orders.
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
	"github.com/EPW80/replenishment-system/internal/materialize"
	"github.com/EPW80/replenishment-system/internal/schedule"
	"github.com/EPW80/replenishment-system/internal/store"
	"github.com/EPW80/replenishment-system/internal/sweep"
)

func main() {
	now := flag.String("now", "", "run as if the current time were this RFC3339 instant; defaults to the real clock")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := config.Load()
	if err != nil {
		log.Error("load config", "error", err)
		os.Exit(1)
	}

	// An instant rather than a date, because resuming asks what day it is in the
	// customer's timezone and a bare date cannot answer that.
	clock := time.Now
	if *now != "" {
		parsed, err := time.Parse(time.RFC3339, *now)
		if err != nil {
			log.Error("invalid -now", "value", *now, "error", err)
			os.Exit(1)
		}
		clock = func() time.Time { return parsed }
	}

	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		log.Error("open database", "error", err)
		os.Exit(1)
	}
	defer func() { _ = db.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	repo := store.New(db)
	// The Service and the Sweeper share one clock: they disagree about what day it is
	// on the boundary otherwise, which is the case this job exists to get right.
	svc := schedule.New(repo, materialize.New(repo, cfg.MaterializeHorizon, log), clock)
	swp := sweep.New(repo, svc, clock, log)

	failed := false

	resumeRes, err := swp.ResumeDue(ctx)
	// Always report what was done, including on a partial failure: the pass continues
	// past a single bad schedule so one customer cannot strand everyone else.
	log.Info("resume sweep complete",
		"considered", resumeRes.Considered,
		"resumed", resumeRes.Resumed,
		"not_yet_due", resumeRes.NotYetDue,
		"already_moved", resumeRes.AlreadyMoved)
	if err != nil {
		log.Error("resume sweep finished with errors", "error", err)
		failed = true
	}

	// Runs regardless of the resume pass's outcome: the two are independent (arming
	// does not depend on any pause having ended today), and skipping this because
	// resume failed would silently delay every customer's pre-billing notice.
	armRes, err := swp.ArmDue(ctx)
	log.Info("arm sweep complete",
		"considered", armRes.Considered,
		"armed", armRes.Armed,
		"not_yet_due", armRes.NotYetDue,
		"already_moved", armRes.AlreadyMoved)
	if err != nil {
		log.Error("arm sweep finished with errors", "error", err)
		failed = true
	}

	if failed {
		os.Exit(1)
	}
}
