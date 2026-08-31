package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The health endpoint's contract with the deploy pipeline: report the commit SHA
// this binary was built from, and fail when a dependency is unusable. The deploy
// health check asserts on that SHA, because a deploy that silently kept serving the
// previous version returns a perfectly good 200.
func TestHealth(t *testing.T) {
	const sha = "3f7a1c9e5b2d8a4f6e0c1b3d5a7f9e2c4b6d8a0f"

	allApplied := func(context.Context, *sql.DB) (bool, int, error) { return true, 0, nil }

	t.Run("reports the build SHA so the probe can verify what is deployed", func(t *testing.T) {
		rec := serve(t, HealthChecker{BuildSHA: sha, MigrationStatus: allApplied})

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var got healthResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.BuildSHA != sha {
			t.Errorf("BuildSHA = %q, want %q", got.BuildSHA, sha)
		}
		if got.Status != "ok" || got.Database != "ok" || got.Migrations != "applied" {
			t.Errorf("unexpected healthy response: %+v", got)
		}
	})

	t.Run("503 when migrations are pending", func(t *testing.T) {
		pending := func(context.Context, *sql.DB) (bool, int, error) { return false, 2, nil }
		rec := serve(t, HealthChecker{BuildSHA: sha, MigrationStatus: pending})

		// Serving traffic against an unmigrated schema is broken in a way a plain
		// 200 cannot show, so the probe must fail rather than report it healthy.
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
		var got healthResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.Migrations != "pending" || got.Pending != 2 {
			t.Errorf("got %+v, want migrations=pending pending=2", got)
		}
	})

	t.Run("503 when the migration state cannot be read", func(t *testing.T) {
		broken := func(context.Context, *sql.DB) (bool, int, error) {
			return false, 0, errors.New("connection reset")
		}
		rec := serve(t, HealthChecker{BuildSHA: sha, MigrationStatus: broken})
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
	})

	t.Run("never caches", func(t *testing.T) {
		// A cached health response could report a dead origin as healthy.
		rec := serve(t, HealthChecker{BuildSHA: sha, MigrationStatus: allApplied})
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q, want no-store", got)
		}
	})
}

// serve runs one request against the health handler with a stubbed database ping.
func serve(t *testing.T, h HealthChecker) *httptest.ResponseRecorder {
	t.Helper()
	db, err := sql.Open("stubdriver", "")
	if err != nil {
		t.Fatalf("open stub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h.DB = db

	rec := httptest.NewRecorder()
	NewRouter(h).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	return rec
}
