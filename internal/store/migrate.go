package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	"github.com/pressly/goose/v3"
)

// migrationFS embeds the SQL migrations into the binary, so the deployed artifact
// carries the exact migrations that were reviewed with it. Nothing has to be copied
// to the host separately, and there is no way for the binary and its migrations to
// disagree about which version they are.
//
//go:embed migrations/*.sql
var migrationFS embed.FS

// Migrate applies all pending migrations.
//
// Callers: the migrate command locally and in CI, and the production deploy step.
// CLAUDE.md requires any schema change to declare database_migration and
// rollback_safe in its release record.
func Migrate(ctx context.Context, db *sql.DB) error {
	goose.SetBaseFS(migrationFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set dialect: %w", err)
	}
	if err := goose.UpContext(ctx, db, "migrations"); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

// MigrationStatus reports whether every embedded migration has been applied.
//
// The health check surfaces this: a deployment serving traffic against a schema it
// has not migrated is broken in a way a plain 200 cannot show.
func MigrationStatus(ctx context.Context, db *sql.DB) (applied bool, pending int, err error) {
	goose.SetBaseFS(migrationFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return false, 0, fmt.Errorf("set dialect: %w", err)
	}
	records, err := goose.CollectMigrations("migrations", 0, goose.MaxVersion)
	if err != nil {
		return false, 0, fmt.Errorf("collect migrations: %w", err)
	}
	current, err := goose.GetDBVersionContext(ctx, db)
	if err != nil {
		return false, 0, fmt.Errorf("read schema version: %w", err)
	}
	for _, m := range records {
		if m.Version > current {
			pending++
		}
	}
	return pending == 0, pending, nil
}
