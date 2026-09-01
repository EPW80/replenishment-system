package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"time"

	"github.com/google/uuid"

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
	CustomerEmail     string         `json:"customer_email"`
	IntervalDays      int            `json:"interval_days"`
	AnchorDate        string         `json:"anchor_date"`
	Timezone          string         `json:"timezone"`
	PaymentTokenRef   string         `json:"payment_token_ref"`
	ShippingAddressID string         `json:"shipping_address_id"`
	DiscountPct       float64        `json:"discount_pct"`
	Items             []itemResponse `json:"items"`
}

// Create handles POST /schedules.
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
	customerEmail, err := parseSingleAddress(req.CustomerEmail)
	if err != nil {
		writeError(w, http.StatusBadRequest, "customer_email must be a single valid email address")
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
		CustomerEmail:     customerEmail,
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
func (h ScheduleHandler) Get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")

	s, err := h.Repo.GetSchedule(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "schedule not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read schedule")
		return
	}

	items, err := h.Repo.ListScheduleItems(ctx, id)
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

	if _, err := h.Repo.GetSchedule(ctx, id); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "schedule not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read schedule")
		return
	}

	occurrences, err := h.Repo.ListOccurrences(ctx, id)
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
func (h ScheduleHandler) ListByCustomer(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	schedules, err := h.Repo.ListSchedulesByCustomer(ctx, r.PathValue("customerID"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read schedules")
		return
	}

	out := make([]scheduleResponse, 0, len(schedules))
	for _, s := range schedules {
		items, err := h.Repo.ListScheduleItems(ctx, s.ID)
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

// parseSingleAddress validates s as exactly one RFC 5322 address using the
// standard library rather than a hand-rolled regex, and returns the normalized
// address — never a "Display Name <addr>" form — since this is what lifecycle
// notifications (spec §7) are sent to.
func parseSingleAddress(s string) (string, error) {
	addr, err := mail.ParseAddress(s)
	if err != nil {
		return "", err
	}
	return addr.Address, nil
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, code int, message string) {
	writeJSON(w, code, map[string]string{"error": message})
}
