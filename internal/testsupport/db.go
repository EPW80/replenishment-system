// Package testsupport provides database fixtures for tests.
//
// It lives under internal/ and is imported only by _test packages, so it is never
// linked into the service binaries.
package testsupport

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/EPW80/replenishment-system/internal/store"
)

// DB returns a migrated database handle isolated in its own Postgres schema.
//
// Isolation matters here specifically: `go test ./...` runs packages in parallel
// against one DATABASE_URL, and the materializer's RunAll operates on *every* active
// schedule. Without a private schema, one package's fixtures appear in another
// package's run and a cleanup delete races an in-flight insert. Sharing a schema
// makes those tests flaky in a way that looks like a bug in the service.
//
// The schema is dropped when the test finishes.
func DB(t *testing.T) *sql.DB {
	t.Helper()

	base := os.Getenv("DATABASE_URL")
	if base == "" {
		t.Skip("DATABASE_URL not set; skipping database integration test")
	}

	schema := "test_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]

	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer func() { _ = admin.Close() }()

	ctx := context.Background()
	if err := admin.PingContext(ctx); err != nil {
		t.Skipf("database unreachable: %v", err)
	}
	if _, err := admin.ExecContext(ctx, fmt.Sprintf(`CREATE SCHEMA %q`, schema)); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	db, err := sql.Open("pgx", withSearchPath(t, base, schema))
	if err != nil {
		t.Fatalf("open scoped connection: %v", err)
	}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	t.Cleanup(func() {
		_ = db.Close()
		cleanup, err := sql.Open("pgx", base)
		if err != nil {
			return
		}
		defer func() { _ = cleanup.Close() }()
		_, _ = cleanup.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS %q CASCADE`, schema))
	})

	return db
}

// withSearchPath returns the connection URL with search_path pointing at schema.
// pgx forwards unrecognized query parameters as Postgres runtime parameters, so this
// applies to every connection the pool opens rather than only the first.
func withSearchPath(t *testing.T, base, schema string) string {
	t.Helper()

	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}
