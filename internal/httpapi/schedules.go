package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/EPW80/replenishment-system/internal/auth"
	"github.com/EPW80/replenishment-system/internal/domain"
	"github.com/EPW80/replenishment-system/internal/store"
)

// ScheduleHandler serves the schedule and upcoming-queue endpoints.
//
// Copy rule, spec §2: every field name and error string here says "when to reorder,"
// never "when to take." Nothing in a response implies consumption — there is no
// usage rate, no supply projection, and no cadence recommendation.
type ScheduleHandler struct {
	Repo store.Repository

	// Now returns the current time; injected so tests are not clock-dependent.
	Now func() time.Time
}

// scheduleResponse is the wire shape of a schedule.
type scheduleResponse struct {
	ID           string  `json:"id"`
	CustomerID   string  `json:"customer_id"`
	Status       string  `json:"status"`
	IntervalDays int     `json:"interval_days"`
	AnchorDate   string  `json:"anchor_date"`
	NextRunDate  *string `json:"next_run_date"`
	Timezone     string  `json:"timezone"`
	DiscountPct  float64 `json:"discount_pct"`
	PausedUntil  *string `json:"paused_until"`

	Items []itemResponse `json:"items"`
}

type itemResponse struct {
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
}

// occurrenceResponse is one entry in the upcoming queue. It carries a date and a
// status and nothing else — deliberately no "days until", which would be a
// supply-depletion projection in disguise.
type occurrenceResponse struct {
	SequenceNo   int     `json:"sequence_no"`
	ScheduledFor string  `json:"scheduled_for"`
	Status       string  `json:"status"`
	OrderID      *string `json:"order_id"`
}

type createScheduleRequest struct {
	CustomerID        string         `json:"customer_id"`
	IntervalDays      int            `json:"interval_days"`
	AnchorDate        string         `json:"anchor_date"`
	Timezone          string         `json:"timezone"`
	PaymentTokenRef   string         `json:"payment_token_ref"`
	ShippingAddressID string         `json:"shipping_address_id"`
	DiscountPct       float64        `json:"discount_pct"`
	Items             []itemResponse `json:"items"`
}

// scopeFor returns the store scope this request may read within, and reports whether
// the caller was authenticated at all.
//
// A missing principal means the route was wired without its middleware. That is a bug
// in the router rather than a request to reject politely, but it still must not fall
// through to an unrestricted read — so it denies, and the caller turns that into a 500.
func scopeFor(r *http.Request) (store.Scope, bool) {
	principal, ok := auth.FromContext(r.Context())
	if !ok {
		return store.CustomerScope(""), false
	}
	if principal.Kind == auth.KindService {
		return store.SystemScope(), true
	}
	return store.CustomerScope(principal.CustomerID), true
}

// Create handles POST /schedules.
//
// customer_id comes from the request body rather than from a token, which is safe only
// because this route requires the service credential: the WP backend is vouching for
// the customer whose checkout it just processed. It would not be safe on a
// customer-token route, where the body is attacker-controlled — that is the reason the
// two live in different route groups.
func (h ScheduleHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createScheduleRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	if req.CustomerID == "" {
		writeError(w, http.StatusBadRequest, "customer_id is required")
		return
	}
	if err := domain.ValidateInterval(req.IntervalDays); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	anchor, err := parseDate(req.AnchorDate)
	if err != nil {
		writeError(w, http.StatusBadRequest, "anchor_date must be YYYY-MM-DD")
		return
	}
	if req.Timezone == "" {
		writeError(w, http.StatusBadRequest, "timezone is required")
		return
	}
	if _, err := time.LoadLocation(req.Timezone); err != nil {
		writeError(w, http.StatusBadRequest, "timezone must be a valid IANA name")
		return
	}
	if len(req.Items) == 0 {
		writeError(w, http.StatusBadRequest, "at least one item is required")
		return
	}

	s := domain.Schedule{
		ID:                uuid.NewString(),
		CustomerID:        req.CustomerID,
		Status:            domain.ScheduleActive,
		IntervalDays:      req.IntervalDays,
		AnchorDate:        anchor,
		Timezone:          req.Timezone,
		PaymentTokenRef:   req.PaymentTokenRef,
		ShippingAddressID: req.ShippingAddressID,
		DiscountPct:       req.DiscountPct,
	}

	items := make([]domain.ScheduleItem, 0, len(req.Items))
	for _, it := range req.Items {
		if it.SKU == "" || it.Quantity < 1 {
			writeError(w, http.StatusBadRequest, "each item needs a sku and a quantity of at least 1")
			return
		}
		items = append(items, domain.ScheduleItem{
			ID: uuid.NewString(), ScheduleID: s.ID, SKU: it.SKU, Quantity: it.Quantity,
		})
	}

	// The schedule and the event recording its creation commit together. Writing them
	// as two calls left a window where a schedule existed with no creation event, and
	// the event log is what the spec §8 read models project from — a schedule missing
	// from it is invisible to every cohort and churn number thereafter.
	ctx := r.Context()
	err = h.Repo.InTx(ctx, func(tx store.Repository) error {
		if err := tx.CreateSchedule(ctx, s, items); err != nil {
			return err
		}
		return tx.AppendEvent(ctx, domain.ScheduleEvent{
			ScheduleID: s.ID,
			EventType:  domain.EventScheduleCreated,
			Actor:      domain.ActorCustomer,
		})
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create schedule")
		return
	}

	writeJSON(w, http.StatusCreated, h.toResponse(s, items))
}

// Get handles GET /schedules/{id}.
//
// A schedule belonging to another customer is a 404, not a 403: the scoped read cannot
// see it, and answering "forbidden" would confirm that the ID names a real schedule.
func (h ScheduleHandler) Get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")

	scope, ok := scopeFor(r)
	if !ok {
		writeError(w, http.StatusInternalServerError, "could not read schedule")
		return
	}

	s, err := h.Repo.GetSchedule(ctx, id, scope)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "schedule not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read schedule")
		return
	}

	items, err := h.Repo.ListScheduleItems(ctx, id, scope)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read schedule items")
		return
	}
	writeJSON(w, http.StatusOK, h.toResponse(s, items))
}

// Upcoming handles GET /schedules/{id}/occurrences — the customer's upcoming queue.
//
// This is the surface spec §5 materializes occurrences for: the customer sees real
// planned shipments and can act on them. It reports when the next order is placed,
// never anything about using the product.
func (h ScheduleHandler) Upcoming(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")

	scope, ok := scopeFor(r)
	if !ok {
		writeError(w, http.StatusInternalServerError, "could not read schedule")
		return
	}

	if _, err := h.Repo.GetSchedule(ctx, id, scope); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "schedule not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read schedule")
		return
	}

	occurrences, err := h.Repo.ListOccurrences(ctx, id, scope)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read upcoming orders")
		return
	}

	out := make([]occurrenceResponse, 0, len(occurrences))
	for _, o := range occurrences {
		out = append(out, occurrenceResponse{
			SequenceNo:   o.SequenceNo,
			ScheduledFor: o.ScheduledFor.String(),
			Status:       string(o.Status),
			OrderID:      o.OrderID,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"occurrences": out})
}

// ListByCustomer handles GET /customers/{customerID}/schedules.
//
// The customer in the path must be the one the token authenticates. Asking for someone
// else's list answers 404 rather than 403, for the same reason as Get: a 403 would
// confirm the customer ID exists.
func (h ScheduleHandler) ListByCustomer(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	customerID := r.PathValue("customerID")

	principal, ok := auth.FromContext(ctx)
	if !ok {
		writeError(w, http.StatusInternalServerError, "could not read schedules")
		return
	}
	if !principal.OwnsCustomer(customerID) {
		writeError(w, http.StatusNotFound, "customer not found")
		return
	}
	scope, ok := scopeFor(r)
	if !ok {
		writeError(w, http.StatusInternalServerError, "could not read schedules")
		return
	}

	schedules, err := h.Repo.ListSchedulesByCustomer(ctx, customerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read schedules")
		return
	}

	out := make([]scheduleResponse, 0, len(schedules))
	for _, s := range schedules {
		items, err := h.Repo.ListScheduleItems(ctx, s.ID, scope)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not read schedule items")
			return
		}
		out = append(out, h.toResponse(s, items))
	}
	writeJSON(w, http.StatusOK, map[string]any{"schedules": out})
}

func (h ScheduleHandler) toResponse(s domain.Schedule, items []domain.ScheduleItem) scheduleResponse {
	resp := scheduleResponse{
		ID:           s.ID,
		CustomerID:   s.CustomerID,
		Status:       string(s.Status),
		IntervalDays: s.IntervalDays,
		AnchorDate:   s.AnchorDate.String(),
		Timezone:     s.Timezone,
		DiscountPct:  s.DiscountPct,
		Items:        make([]itemResponse, 0, len(items)),
	}
	if s.NextRunDate != nil {
		v := s.NextRunDate.String()
		resp.NextRunDate = &v
	}
	if s.PausedUntil != nil {
		v := s.PausedUntil.String()
		resp.PausedUntil = &v
	}
	for _, it := range items {
		resp.Items = append(resp.Items, itemResponse{SKU: it.SKU, Quantity: it.Quantity})
	}
	return resp
}

func parseDate(s string) (domain.Date, error) {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return domain.Date{}, err
	}
	return domain.DateOf(t), nil
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, code int, message string) {
	writeJSON(w, code, map[string]string{"error": message})
}
