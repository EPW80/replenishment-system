package httpapi_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EPW80/replenishment-system/internal/httpapi"
	"github.com/EPW80/replenishment-system/internal/materialize"
	"github.com/EPW80/replenishment-system/internal/schedule"
	"github.com/EPW80/replenishment-system/internal/store"
	"github.com/EPW80/replenishment-system/internal/testsupport"
)

func newAPI(t *testing.T) (http.Handler, *store.PostgresRepository, *sql.DB) {
	t.Helper()

	db := testsupport.DB(t)
	repo := store.New(db)
	h := httpapi.NewServiceRouter(
		httpapi.HealthChecker{DB: db, BuildSHA: "test", MigrationStatus: store.MigrationStatus},
		httpapi.ScheduleHandler{Repo: repo, Now: time.Now},
		httpapi.TransitionHandler{
			Service: schedule.New(repo, materialize.New(repo, 3, nil), time.Now),
			Repo:    repo,
		},
	)
	return h, repo, db
}

func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestCreateAndGetSchedule(t *testing.T) {
	h, _, _ := newAPI(t)
	customer := "cust_" + uuid.NewString()[:8]

	body := `{
		"customer_id": "` + customer + `",
		"customer_email": "` + customer + `@example.com",
		"interval_days": 30,
		"anchor_date": "2026-01-01",
		"timezone": "America/Los_Angeles",
		"payment_token_ref": "tok_abc123",
		"shipping_address_id": "addr_1",
		"discount_pct": 10,
		"items": [{"sku": "SKU-001", "quantity": 2}]
	}`

	rec := do(t, h, http.MethodPost, "/schedules", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var created map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatal("no id in response")
	}
	if created["interval_days"].(float64) != 30 {
		t.Errorf("interval_days = %v", created["interval_days"])
	}
	if created["anchor_date"] != "2026-01-01" {
		t.Errorf("anchor_date = %v", created["anchor_date"])
	}

	// The vault reference must never be echoed back to a client.
	if strings.Contains(rec.Body.String(), "tok_abc123") {
		t.Error("payment_token_ref leaked into the response")
	}
	// Same for the email address — write-only, like the vault reference.
	if strings.Contains(rec.Body.String(), customer+"@example.com") {
		t.Error("customer_email leaked into the response")
	}

	get := do(t, h, http.MethodGet, "/schedules/"+id, "")
	if get.Code != http.StatusOK {
		t.Fatalf("get status = %d", get.Code)
	}
	var fetched map[string]any
	_ = json.Unmarshal(get.Body.Bytes(), &fetched)
	if fetched["id"] != id {
		t.Errorf("round trip lost the id")
	}
}

func TestCreateScheduleValidation(t *testing.T) {
	h, _, _ := newAPI(t)

	valid := map[string]any{
		"customer_id":    "cust_1",
		"customer_email": "cust_1@example.com",
		"interval_days":  30,
		"anchor_date":    "2026-01-01",
		"timezone":       "UTC",
		"items":          []map[string]any{{"sku": "SKU-001", "quantity": 1}},
	}

	for _, tc := range []struct {
		name  string
		patch func(map[string]any)
	}{
		{"missing customer_id", func(m map[string]any) { delete(m, "customer_id") }},
		{"missing customer_email", func(m map[string]any) { delete(m, "customer_email") }},
		{"malformed customer_email", func(m map[string]any) { m["customer_email"] = "not-an-email" }},
		{"multiple addresses in customer_email are rejected", func(m map[string]any) {
			m["customer_email"] = "Name <cust_1@example.com>, second@example.com"
		}},
		{"interval below spec minimum", func(m map[string]any) { m["interval_days"] = 6 }},
		{"interval above spec maximum", func(m map[string]any) { m["interval_days"] = 181 }},
		{"malformed anchor_date", func(m map[string]any) { m["anchor_date"] = "01/01/2026" }},
		{"missing timezone", func(m map[string]any) { delete(m, "timezone") }},
		{"invalid timezone", func(m map[string]any) { m["timezone"] = "Mars/Olympus_Mons" }},
		{"no items", func(m map[string]any) { m["items"] = []map[string]any{} }},
		{"zero quantity", func(m map[string]any) {
			m["items"] = []map[string]any{{"sku": "S", "quantity": 0}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := map[string]any{}
			for k, v := range valid {
				payload[k] = v
			}
			tc.patch(payload)
			b, _ := json.Marshal(payload)

			rec := do(t, h, http.MethodPost, "/schedules", string(b))
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
			}
		})
	}
}

// An unknown field is rejected rather than silently ignored -- a client sending
// doses_per_day should get an error, not have it quietly dropped.
func TestCreateScheduleRejectsUnknownFields(t *testing.T) {
	h, _, _ := newAPI(t)

	body := `{
		"customer_id": "c1", "interval_days": 30, "anchor_date": "2026-01-01",
		"timezone": "UTC", "items": [{"sku":"S","quantity":1}],
		"doses_per_day": 2
	}`
	rec := do(t, h, http.MethodPost, "/schedules", body)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 — unknown fields must be rejected", rec.Code)
	}
}

func TestGetMissingScheduleReturns404(t *testing.T) {
	h, _, _ := newAPI(t)

	rec := do(t, h, http.MethodGet, "/schedules/"+uuid.NewString(), "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	rec = do(t, h, http.MethodGet, "/schedules/"+uuid.NewString()+"/occurrences", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("occurrences status = %d, want 404", rec.Code)
	}
}

// The upcoming queue reports dates and statuses. It must not carry anything that
// implies consumption -- no days-remaining, no usage rate, no recommendation.
func TestUpcomingQueueCarriesNoConsumptionFields(t *testing.T) {
	h, _, _ := newAPI(t)
	customer := "cust_" + uuid.NewString()[:8]

	body := `{"customer_id":"` + customer + `","customer_email":"` + customer + `@example.com","interval_days":30,"anchor_date":"2026-01-01",
		"timezone":"UTC","items":[{"sku":"SKU-001","quantity":1}]}`
	rec := do(t, h, http.MethodPost, "/schedules", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create failed: %s", rec.Body.String())
	}
	var created map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	id := created["id"].(string)

	got := do(t, h, http.MethodGet, "/schedules/"+id+"/occurrences", "")
	if got.Code != http.StatusOK {
		t.Fatalf("status = %d", got.Code)
	}

	// Spec §2: the response must not store, compute, infer or display consumption.
	for _, banned := range []string{
		"doses", "dosage", "per_day", "remaining", "days_left", "adherence",
		"intake", "supply", "recommended", "when_to_take",
	} {
		if strings.Contains(strings.ToLower(got.Body.String()), banned) {
			t.Errorf("upcoming queue response contains %q — spec §2 forbids it\nbody: %s",
				banned, got.Body.String())
		}
	}
}

func TestListSchedulesByCustomer(t *testing.T) {
	h, _, _ := newAPI(t)
	customer := "cust_" + uuid.NewString()[:8]

	for _, sku := range []string{"SKU-001", "SKU-002"} {
		body := `{"customer_id":"` + customer + `","customer_email":"` + customer + `@example.com","interval_days":30,"anchor_date":"2026-01-01",
			"timezone":"UTC","items":[{"sku":"` + sku + `","quantity":1}]}`
		if rec := do(t, h, http.MethodPost, "/schedules", body); rec.Code != http.StatusCreated {
			t.Fatalf("create %s: %s", sku, rec.Body.String())
		}
	}

	rec := do(t, h, http.MethodGet, "/customers/"+customer+"/schedules", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var out struct {
		Schedules []map[string]any `json:"schedules"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Schedules) != 2 {
		t.Errorf("got %d schedules, want 2", len(out.Schedules))
	}
}
