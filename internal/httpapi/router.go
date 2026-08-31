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
func NewServiceRouter(h HealthChecker, s ScheduleHandler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.Health)

	mux.HandleFunc("POST /schedules", s.Create)
	mux.HandleFunc("GET /schedules/{id}", s.Get)
	mux.HandleFunc("GET /schedules/{id}/occurrences", s.Upcoming)
	mux.HandleFunc("GET /customers/{customerID}/schedules", s.ListByCustomer)

	return mux
}
