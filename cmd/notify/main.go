// Command notify runs one lifecycle-email dispatch pass (spec §7): schedule
// created, and paused / resumed / canceled. It is a transactional outbox off
// schedule_events, so running it twice sends nothing twice for the same event.
//
// Intended to run on a schedule, same operational shape as cmd/materialize.
//
// Pre-billing notice, order-placed, and the dunning ladder are not sent here —
// see docs/adr/0007 for why they need Phase 2.
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

	// Validated here, not in config.Load: a service or job that never sends email
	// should not have to set Postmark credentials just to start.
	if cfg.PostmarkServerToken == "" {
		log.Error("POSTMARK_SERVER_TOKEN is required")
		os.Exit(1)
	}
	if cfg.EmailFromAddress == "" {
		log.Error("EMAIL_FROM_ADDRESS is required")
		os.Exit(1)
	}
	if cfg.EmailSupportAddress == "" {
		log.Error("EMAIL_SUPPORT_ADDRESS is required")
		os.Exit(1)
	}

	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		log.Error("open database", "error", err)
		os.Exit(1)
	}
	defer func() { _ = db.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	dispatcher := &notify.Dispatcher{
		Repo: store.New(db),
		Sender: &notify.PostmarkSender{
			ServerToken: cfg.PostmarkServerToken,
			FromAddress: cfg.EmailFromAddress,
		},
		SupportEmail: cfg.EmailSupportAddress,
		Log:          log,
	}

	res, err := dispatcher.RunOnce(ctx)

	// Always report what was done. A partial failure still means real emails went
	// out — that count matters as much as the failure count.
	log.Info("notification dispatch complete", "sent", res.Sent, "failed", res.Failed)

	if err != nil {
		log.Error("dispatch finished with errors", "error", err)
		os.Exit(1)
	}
	if res.Failed > 0 {
		os.Exit(1)
	}
}
