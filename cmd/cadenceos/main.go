// Command cadenceos runs the CadenceOS replenishment scheduling service.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/EPW80/replenishment-system/internal/auth"
	"github.com/EPW80/replenishment-system/internal/config"
	"github.com/EPW80/replenishment-system/internal/httpapi"
	"github.com/EPW80/replenishment-system/internal/materialize"
	"github.com/EPW80/replenishment-system/internal/schedule"
	"github.com/EPW80/replenishment-system/internal/store"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	if err := run(); err != nil {
		log.Error("service stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Checked here rather than in Load: this is the only binary that authenticates a
	// caller, and it must not bind a port without the credentials to do it. See
	// config.RequireAuth.
	if err := cfg.RequireAuth(); err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Signal-aware from the start, so a shutdown during boot is honoured.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = db.Close() }()

	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Hour)

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}

	// The transitions in spec §6 re-materialize as part of their own transaction, so
	// the service shares the horizon the nightly job uses. One definition, not two.
	repo := store.New(db)
	materializer := materialize.New(repo, cfg.MaterializeHorizon, slog.Default())

	// Credentials are verified per request against these; config refuses to start
	// without them, so there is no unauthenticated mode to fall back into.
	middleware := httpapi.Middleware{
		Tokens: auth.NewTokenVerifier(auth.TokenConfig{
			Secret:   cfg.PortalJWTSecret,
			Issuer:   cfg.PortalJWTIssuer,
			Audience: cfg.PortalJWTAudience,
		}),
		ServiceKey: auth.NewServiceKeyVerifier(cfg.ServiceAPIKey),
		Log:        slog.Default(),
	}

	srv := &http.Server{
		Addr: fmt.Sprintf(":%d", cfg.Port),
		Handler: httpapi.NewServiceRouter(
			httpapi.HealthChecker{
				DB:              db,
				BuildSHA:        cfg.BuildSHA,
				MigrationStatus: store.MigrationStatus,
			},
			httpapi.ScheduleHandler{Repo: repo, Now: time.Now},
			httpapi.TransitionHandler{
				Service: schedule.New(repo, materializer, time.Now),
				Repo:    repo,
			},
			middleware,
		),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", srv.Addr, "build_sha", cfg.BuildSHA)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		slog.Info("shutdown signal received, draining")
	}

	// Graceful shutdown. This service creates orders and moves money; a request
	// cut off mid-flight is a materially worse outcome than a slow shutdown.
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelShutdown()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	slog.Info("stopped cleanly")
	return nil
}
