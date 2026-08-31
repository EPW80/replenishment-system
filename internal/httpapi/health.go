package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"time"
)

// HealthChecker reports whether the service's dependencies are usable.
type HealthChecker struct {
	DB       *sql.DB
	BuildSHA string

	// MigrationStatus reports whether all migrations have been applied. Injected so
	// this package does not depend on the store package.
	MigrationStatus func(ctx context.Context, db *sql.DB) (bool, int, error)
}

type healthResponse struct {
	Status     string `json:"status"`
	BuildSHA   string `json:"build_sha"`
	Database   string `json:"database"`
	Migrations string `json:"migrations"`
	Pending    int    `json:"pending_migrations,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

// Health handles GET /healthz.
//
// It reports the commit SHA this binary was built from, because the deploy health
// check asserts on it. A deploy that silently kept serving the previous version
// returns 200 with the previous SHA — the failure a plain 200 cannot see. It also
// checks the database is reachable and the schema is migrated, so "healthy" means
// the service can actually do its job rather than merely that the process is up.
//
// Returns 503 on any failure, so the probe fails rather than reporting a broken
// deployment as good.
func (h HealthChecker) Health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	resp := healthResponse{
		Status:     "ok",
		BuildSHA:   h.BuildSHA,
		Database:   "ok",
		Migrations: "applied",
	}
	code := http.StatusOK

	if err := h.DB.PingContext(ctx); err != nil {
		resp.Status = "unhealthy"
		resp.Database = "unreachable"
		resp.Migrations = "unknown"
		resp.Detail = "database unreachable"
		code = http.StatusServiceUnavailable
	} else if h.MigrationStatus != nil {
		applied, pending, err := h.MigrationStatus(ctx, h.DB)
		switch {
		case err != nil:
			resp.Status = "unhealthy"
			resp.Migrations = "unknown"
			resp.Detail = err.Error()
			code = http.StatusServiceUnavailable
		case !applied:
			resp.Status = "unhealthy"
			resp.Migrations = "pending"
			resp.Pending = pending
			code = http.StatusServiceUnavailable
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(resp)
}
