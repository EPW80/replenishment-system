// Package httpapi holds the service's HTTP handlers and middleware.
package httpapi

import "net/http"

// NewRouter wires the service's routes.
func NewRouter(h HealthChecker) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.Health)
	return mux
}
