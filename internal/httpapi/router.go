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
//
// Routes are grouped by the credential they require, and the grouping is the security
// boundary rather than a tidiness preference:
//
//   - public: /healthz, which a deploy probe must reach before any credential exists.
//   - customer: everything a person does to their own schedules, behind a portal token.
//   - service: schedule creation, which happens server-side at WooCommerce checkout
//     where there is no customer session to mint a token from.
//
// The Phase 6 admin surface gets its own group here rather than reusing the customer
// one; it is deliberately absent until that phase, because a route group with no
// routes is clearer than an admin path nobody has designed yet.
func NewServiceRouter(h HealthChecker, s ScheduleHandler, t TransitionHandler, mw Middleware) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", h.Health)

	customer := func(pattern string, fn http.HandlerFunc) {
		mux.Handle(pattern, mw.RequireCustomer(fn))
	}
	service := func(pattern string, fn http.HandlerFunc) {
		mux.Handle(pattern, mw.RequireService(fn))
	}

	service("POST /schedules", s.Create)

	customer("GET /schedules/{id}", s.Get)
	customer("GET /schedules/{id}/occurrences", s.Upcoming)
	customer("GET /customers/{customerID}/schedules", s.ListByCustomer)

	// Spec §6 transitions. POST rather than PATCH: each is a named action with its
	// own preconditions and its own audit event, not a partial update of a resource.
	customer("POST /schedules/{id}/pause", t.Pause)
	customer("POST /schedules/{id}/resume", t.Resume)
	customer("POST /schedules/{id}/skip", t.Skip)
	customer("POST /schedules/{id}/defer", t.Defer)
	customer("POST /schedules/{id}/cadence", t.Cadence)
	customer("POST /schedules/{id}/cancel", t.Cancel)

	return mux
}
