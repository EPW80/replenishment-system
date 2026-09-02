package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/EPW80/replenishment-system/internal/auth"
	"github.com/EPW80/replenishment-system/internal/domain"
	"github.com/EPW80/replenishment-system/internal/schedule"
	"github.com/EPW80/replenishment-system/internal/store"
)

// TransitionHandler serves the spec §6 schedule actions.
//
// Copy rule, spec §2: every error string here describes the *schedule* and when its
// next *order* is placed. None of them refers to the product being used or needed.
type TransitionHandler struct {
	Service TransitionService

	// Repo reads the items back for the response body.
	Repo store.Repository
}

// TransitionService is the state machine this handler drives. Declared as an interface
// so handler tests do not need a database.
type TransitionService interface {
	Pause(ctx context.Context, scheduleID string, until *domain.Date, caller schedule.Caller) (domain.Schedule, error)
	Resume(ctx context.Context, scheduleID string, caller schedule.Caller) (domain.Schedule, error)
	SkipNext(ctx context.Context, scheduleID string, caller schedule.Caller) (domain.Schedule, error)
	Defer(ctx context.Context, scheduleID string, days int, caller schedule.Caller) (domain.Schedule, error)
	ChangeCadence(ctx context.Context, scheduleID string, intervalDays int, caller schedule.Caller) (domain.Schedule, error)
	Cancel(ctx context.Context, scheduleID, reasonCode string, caller schedule.Caller) (domain.Schedule, error)
}

// decode reads a JSON body that may legitimately be absent.
//
// pause, resume and skip take no required fields, and a client sending no body at all
// for those is well-behaved rather than wrong.
func decode(w http.ResponseWriter, r *http.Request, dst any) error {
	if r.ContentLength == 0 {
		return nil
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	return nil
}

// respond turns a service result into a response.
//
// The status mapping is the point: a precondition failure is 409, not 400. The request
// was well-formed and the customer is allowed to make it — the schedule is simply in a
// state that cannot accept it, and a client that retries identically after a pause
// will succeed.
func (h TransitionHandler) respond(w http.ResponseWriter, r *http.Request, s domain.Schedule, err error, c schedule.Caller) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "schedule not found")
		return
	case domain.IsTransitionError(err):
		var te *domain.TransitionError
		errors.As(err, &te)
		writeError(w, http.StatusConflict, te.CustomerMessage())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "could not update this schedule")
		return
	}

	items, err := h.Repo.ListScheduleItems(r.Context(), s.ID, c.Scope)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read schedule items")
		return
	}
	writeJSON(w, http.StatusOK, ScheduleHandler{}.toResponse(s, items))
}

// caller builds the verified identity a transition runs under.
//
// This used to be a constant asserting every request came from a customer. It was true
// of the traffic the service expected and false of the traffic it would accept: with no
// authentication, anyone could send these requests and be recorded as the customer.
// Both the actor in the audit log and the scope the transition may touch now come from
// the verified credential.
//
// The false return means the route was wired without its middleware — a router bug, so
// the handler answers 500 rather than guessing an identity.
func caller(r *http.Request) (schedule.Caller, bool) {
	principal, ok := auth.FromContext(r.Context())
	if !ok {
		return schedule.Caller{}, false
	}
	scope, ok := scopeFor(r)
	if !ok {
		return schedule.Caller{}, false
	}
	return schedule.Caller{Actor: principal.Actor(), Scope: scope}, true
}

type pauseRequest struct {
	PausedUntil string `json:"paused_until"`
}

// Pause handles POST /schedules/{id}/pause.
func (h TransitionHandler) Pause(w http.ResponseWriter, r *http.Request) {
	var req pauseRequest
	if err := decode(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	var until *domain.Date
	if req.PausedUntil != "" {
		d, err := parseDate(req.PausedUntil)
		if err != nil {
			writeError(w, http.StatusBadRequest, "paused_until must be YYYY-MM-DD")
			return
		}
		until = &d
	}

	c, ok := caller(r)
	if !ok {
		writeError(w, http.StatusInternalServerError, "could not update this schedule")
		return
	}

	s, err := h.Service.Pause(r.Context(), r.PathValue("id"), until, c)
	h.respond(w, r, s, err, c)
}

// Resume handles POST /schedules/{id}/resume.
func (h TransitionHandler) Resume(w http.ResponseWriter, r *http.Request) {
	c, ok := caller(r)
	if !ok {
		writeError(w, http.StatusInternalServerError, "could not update this schedule")
		return
	}

	s, err := h.Service.Resume(r.Context(), r.PathValue("id"), c)
	h.respond(w, r, s, err, c)
}

// Skip handles POST /schedules/{id}/skip — skip the next order, keep the schedule.
func (h TransitionHandler) Skip(w http.ResponseWriter, r *http.Request) {
	c, ok := caller(r)
	if !ok {
		writeError(w, http.StatusInternalServerError, "could not update this schedule")
		return
	}

	s, err := h.Service.SkipNext(r.Context(), r.PathValue("id"), c)
	h.respond(w, r, s, err, c)
}

type deferRequest struct {
	Days int `json:"days"`
}

// Defer handles POST /schedules/{id}/defer.
func (h TransitionHandler) Defer(w http.ResponseWriter, r *http.Request) {
	var req deferRequest
	if err := decode(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := domain.ValidateDeferDays(req.Days); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	c, ok := caller(r)
	if !ok {
		writeError(w, http.StatusInternalServerError, "could not update this schedule")
		return
	}

	s, err := h.Service.Defer(r.Context(), r.PathValue("id"), req.Days, c)
	h.respond(w, r, s, err, c)
}

type cadenceRequest struct {
	IntervalDays int `json:"interval_days"`
}

// Cadence handles POST /schedules/{id}/cadence — change how many days apart orders are
// placed. Days between shipments is the only cadence this service knows (spec §2).
func (h TransitionHandler) Cadence(w http.ResponseWriter, r *http.Request) {
	var req cadenceRequest
	if err := decode(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := domain.ValidateInterval(req.IntervalDays); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	c, ok := caller(r)
	if !ok {
		writeError(w, http.StatusInternalServerError, "could not update this schedule")
		return
	}

	s, err := h.Service.ChangeCadence(r.Context(), r.PathValue("id"), req.IntervalDays, c)
	h.respond(w, r, s, err, c)
}

type cancelRequest struct {
	ReasonCode string `json:"reason_code"`
}

// Cancel handles POST /schedules/{id}/cancel.
func (h TransitionHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	var req cancelRequest
	if err := decode(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := domain.ValidateCancellationReason(req.ReasonCode); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	c, ok := caller(r)
	if !ok {
		writeError(w, http.StatusInternalServerError, "could not update this schedule")
		return
	}

	s, err := h.Service.Cancel(r.Context(), r.PathValue("id"), req.ReasonCode, c)
	h.respond(w, r, s, err, c)
}
