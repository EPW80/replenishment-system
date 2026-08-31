package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/EPW80/replenishment-system/internal/domain"
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
	Pause(ctx context.Context, scheduleID string, until *domain.Date, actor domain.EventActor) (domain.Schedule, error)
	Resume(ctx context.Context, scheduleID string, actor domain.EventActor) (domain.Schedule, error)
	SkipNext(ctx context.Context, scheduleID string, actor domain.EventActor) (domain.Schedule, error)
	Defer(ctx context.Context, scheduleID string, days int, actor domain.EventActor) (domain.Schedule, error)
	ChangeCadence(ctx context.Context, scheduleID string, intervalDays int, actor domain.EventActor) (domain.Schedule, error)
	Cancel(ctx context.Context, scheduleID, reasonCode string, actor domain.EventActor) (domain.Schedule, error)
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
func (h TransitionHandler) respond(w http.ResponseWriter, r *http.Request, s domain.Schedule, err error) {
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

	items, err := h.Repo.ListScheduleItems(r.Context(), s.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read schedule items")
		return
	}
	writeJSON(w, http.StatusOK, ScheduleHandler{}.toResponse(s, items))
}

// actor records who caused a transition.
//
// Every request that reaches this handler arrives through the portal's authenticated
// proxy (spec §4), so the actor is the customer. Admin- and system-initiated
// transitions do not come through here; when an admin surface lands in Phase 6 it will
// pass its own actor rather than reusing this one.
const actor = domain.ActorCustomer

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

	s, err := h.Service.Pause(r.Context(), r.PathValue("id"), until, actor)
	h.respond(w, r, s, err)
}

// Resume handles POST /schedules/{id}/resume.
func (h TransitionHandler) Resume(w http.ResponseWriter, r *http.Request) {
	s, err := h.Service.Resume(r.Context(), r.PathValue("id"), actor)
	h.respond(w, r, s, err)
}

// Skip handles POST /schedules/{id}/skip — skip the next order, keep the schedule.
func (h TransitionHandler) Skip(w http.ResponseWriter, r *http.Request) {
	s, err := h.Service.SkipNext(r.Context(), r.PathValue("id"), actor)
	h.respond(w, r, s, err)
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

	s, err := h.Service.Defer(r.Context(), r.PathValue("id"), req.Days, actor)
	h.respond(w, r, s, err)
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

	s, err := h.Service.ChangeCadence(r.Context(), r.PathValue("id"), req.IntervalDays, actor)
	h.respond(w, r, s, err)
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

	s, err := h.Service.Cancel(r.Context(), r.PathValue("id"), req.ReasonCode, actor)
	h.respond(w, r, s, err)
}
