// Command migrate applies pending database migrations.
//
// Run by `make migrate` locally and in CI, and by the production deploy step.
// Kept separate from the service binary so migrations are an explicit, gated
// action rather than a side effect of a process starting.
package main

import (
	"context"
	"database/sql"
	"log"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/EPW80/replenishment-system/internal/store"
)

func main() {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		log.Fatal("DATABASE_URL is required")
	}

	db, err := sql.Open("pgx", url)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err := store.Migrate(ctx, db); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	log.Println("migrations applied")
}
