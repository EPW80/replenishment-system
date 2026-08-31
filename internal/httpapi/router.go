// Package httpapi holds the service's HTTP handlers and middleware.
package httpapi

import "net/http"

// NewRouter wires the service's routes.
func NewRouter(h HealthChecker) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.Health)
	return mux
}

// NewServiceRouter wires the health endpoint together with the schedule endpoints.
func NewServiceRouter(h HealthChecker, s ScheduleHandler, t TransitionHandler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.Health)

	mux.HandleFunc("POST /schedules", s.Create)
	mux.HandleFunc("GET /schedules/{id}", s.Get)
	mux.HandleFunc("GET /schedules/{id}/occurrences", s.Upcoming)
	mux.HandleFunc("GET /customers/{customerID}/schedules", s.ListByCustomer)

	// Spec §6 transitions. POST rather than PATCH: each is a named action with its
	// own preconditions and its own audit event, not a partial update of a resource.
	mux.HandleFunc("POST /schedules/{id}/pause", t.Pause)
	mux.HandleFunc("POST /schedules/{id}/resume", t.Resume)
	mux.HandleFunc("POST /schedules/{id}/skip", t.Skip)
	mux.HandleFunc("POST /schedules/{id}/defer", t.Defer)
	mux.HandleFunc("POST /schedules/{id}/cadence", t.Cadence)
	mux.HandleFunc("POST /schedules/{id}/cancel", t.Cancel)

	return mux
}
