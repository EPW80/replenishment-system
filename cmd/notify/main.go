// Command notify sends the Phase 4 transactional emails (spec §7): schedule created,
// paused, resumed, and canceled.
//
// Runs on its own five-minute schedule, deliberately not alongside materialize and
// sweep in scripts/nightly.sh: spec §7 gives these sends a Timing of "immediate", which
// a nightly pass cannot honour, and this is the one job that needs Postmark credentials
// the other two never present. See docs/adr/0012.
//
// It is safe to run concurrently with itself or retry after a failure: delivery is
// at-least-once, not exactly-once (docs/adr/0010), so a duplicate confirmation email is
// the accepted cost rather than something this command works to prevent. Overlapping
// runs need no coordination — ClaimNotifiableEvents skips rows another run holds.
//
// Unlike materialize and sweep, this is the one job that needs Postmark configured —
// config.Load leaves POSTMARK_API_KEY, NOTIFICATION_FROM_ADDRESS and
// NOTIFICATION_SUPPORT_CONTACT unchecked for every other binary, so this command
// validates them itself via config.Config.RequireNotifications.
package main

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/EPW80/replenishment-system/internal/config"
	"github.com/EPW80/replenishment-system/internal/notify"
	"github.com/EPW80/replenishment-system/internal/store"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := config.Load()
	if err != nil {
		log.Error("load config", "error", err)
		os.Exit(1)
	}
	if err := cfg.RequireNotifications(); err != nil {
		log.Error("load notification config", "error", err)
		os.Exit(1)
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
	sender := notify.NewPostmarkSender(cfg.PostmarkAPIKey, cfg.NotificationFromAddress)
	dispatcher := notify.New(repo, sender, cfg.NotificationSupportContact, log)

	res, err := dispatcher.RunAll(ctx)

	// Always report what was done, including on a partial failure: the pass continues
	// past a single bad address so one customer cannot strand everyone else's
	// confirmation.
	log.Info("notification dispatch complete",
		"claimed", res.Claimed,
		"sent", res.Sent,
		"skipped", res.Skipped,
		"superseded", res.Superseded,
		"send_failed", res.SendFailed)

	if err != nil {
		log.Error("notification dispatch finished with errors", "error", err)
		os.Exit(1)
	}
}
