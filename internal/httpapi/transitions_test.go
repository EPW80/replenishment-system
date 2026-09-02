package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EPW80/replenishment-system/internal/domain"
	"github.com/EPW80/replenishment-system/internal/httpapi"
	"github.com/EPW80/replenishment-system/internal/materialize"
	"github.com/EPW80/replenishment-system/internal/schedule"
	"github.com/EPW80/replenishment-system/internal/store"
)

// newScheduleWithHorizon creates an active schedule with occurrences planned, which is
// the state the skip and defer endpoints act on.
func newScheduleWithHorizon(t *testing.T, repo *store.PostgresRepository) domain.Schedule {
	t.Helper()
	ctx := context.Background()

	s := domain.Schedule{
		ID:           uuid.NewString(),
		CustomerID:   "cust_" + uuid.NewString()[:8],
		Status:       domain.ScheduleActive,
		IntervalDays: 30,
		AnchorDate:   domain.DateOf(time.Now().UTC()),
		Timezone:     "UTC",
	}
	if err := repo.CreateSchedule(ctx, s, []domain.ScheduleItem{
		{ID: uuid.NewString(), ScheduleID: s.ID, SKU: "SKU-001", Quantity: 1},
	}); err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	if _, _, err := materialize.New(repo, 3, nil).Run(ctx, s, domain.DateOf(time.Now().UTC())); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	return s
}

func decodeSchedule(t *testing.T, body string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode response: %v (body %s)", err, body)
	}
	return out
}

func TestTransitionEndpointsHappyPath(t *testing.T) {
	h, repo, _ := newAPI(t)

	tests := []struct {
		name       string
		path       string
		body       string
		wantStatus string
	}{
		{"pause", "/pause", `{}`, "paused"},
		{"pause with a resume date", "/pause", `{"paused_until":"2030-01-01"}`, "paused"},
		{"skip", "/skip", ``, "active"},
		{"defer", "/defer", `{"days":7}`, "active"},
		{"change cadence", "/cadence", `{"interval_days":60}`, "active"},
		{"cancel", "/cancel", `{"reason_code":"too_expensive"}`, "canceled"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newScheduleWithHorizon(t, repo)
			rec := do(t, h, http.MethodPost, "/schedules/"+s.ID+tc.path, tc.body, customerCred(t, s.CustomerID))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			got := decodeSchedule(t, rec.Body.String())
			if got["status"] != tc.wantStatus {
				t.Errorf("schedule status = %v, want %s", got["status"], tc.wantStatus)
			}
			// The response is the full schedule, items included, so the portal can
			// re-render without a second request.
			if _, ok := got["items"]; !ok {
				t.Error("response has no items field")
			}
		})
	}
}

func TestResumeEndpoint(t *testing.T) {
	h, repo, _ := newAPI(t)
	s := newScheduleWithHorizon(t, repo)

	if rec := do(t, h, http.MethodPost, "/schedules/"+s.ID+"/pause", `{}`, customerCred(t, s.CustomerID)); rec.Code != http.StatusOK {
		t.Fatalf("pause: %d %s", rec.Code, rec.Body.String())
	}
	rec := do(t, h, http.MethodPost, "/schedules/"+s.ID+"/resume", ``, customerCred(t, s.CustomerID))
	if rec.Code != http.StatusOK {
		t.Fatalf("resume: %d %s", rec.Code, rec.Body.String())
	}
	if got := decodeSchedule(t, rec.Body.String()); got["status"] != "active" {
		t.Errorf("status = %v, want active", got["status"])
	}
}

// A precondition failure is 409, not 400. The request is well-formed and the customer
// is allowed to make it — the schedule is simply in a state that cannot accept it, and
// an identical retry after a resume will succeed.
func TestFailedPreconditionIsConflict(t *testing.T) {
	h, repo, _ := newAPI(t)

	tests := []struct {
		name  string
		setup string
		path  string
		body  string
	}{
		{"pause an already paused schedule", "/pause", "/pause", `{}`},
		{"resume an active schedule", "", "/resume", ``},
		{"skip on a paused schedule", "/pause", "/skip", ``},
		{"defer on a paused schedule", "/pause", "/defer", `{"days":7}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newScheduleWithHorizon(t, repo)
			if tc.setup != "" {
				if rec := do(t, h, http.MethodPost, "/schedules/"+s.ID+tc.setup, `{}`, customerCred(t, s.CustomerID)); rec.Code != http.StatusOK {
					t.Fatalf("setup %s: %d %s", tc.setup, rec.Code, rec.Body.String())
				}
			}
			rec := do(t, h, http.MethodPost, "/schedules/"+s.ID+tc.path, tc.body, customerCred(t, s.CustomerID))
			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409; body = %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "error") {
				t.Errorf("409 body carries no error message: %s", rec.Body.String())
			}
		})
	}
}

// Everything a customer can be told follows spec §2's copy rule: it describes the
// schedule and its next order, never the product's use.
func TestErrorCopyFollowsTheComplianceBoundary(t *testing.T) {
	h, repo, _ := newAPI(t)
	s := newScheduleWithHorizon(t, repo)
	if rec := do(t, h, http.MethodPost, "/schedules/"+s.ID+"/cancel", `{"reason_code":"other"}`, customerCred(t, s.CustomerID)); rec.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body.String())
	}

	banned := []string{"dose", "dosage", "take", "supply", "remaining", "run out", "intake", "consumption"}
	for _, path := range []string{"/pause", "/resume", "/skip", "/defer", "/cadence", "/cancel"} {
		body := `{}`
		switch path {
		case "/defer":
			body = `{"days":7}`
		case "/cadence":
			body = `{"interval_days":60}`
		case "/cancel":
			body = `{"reason_code":"other"}`
		}
		rec := do(t, h, http.MethodPost, "/schedules/"+s.ID+path, body, customerCred(t, s.CustomerID))
		msg := strings.ToLower(rec.Body.String())
		for _, w := range banned {
			if strings.Contains(msg, w) {
				t.Errorf("%s response contains %q: %s", path, w, rec.Body.String())
			}
		}
	}
}

func TestMalformedRequestsAreBadRequest(t *testing.T) {
	h, repo, _ := newAPI(t)

	tests := []struct {
		name string
		path string
		body string
	}{
		{"defer with no days", "/defer", `{}`},
		{"defer with zero days", "/defer", `{"days":0}`},
		{"defer with a negative", "/defer", `{"days":-5}`},
		{"defer beyond the limit", "/defer", `{"days":900}`},
		{"cadence below the range", "/cadence", `{"interval_days":3}`},
		{"cadence above the range", "/cadence", `{"interval_days":400}`},
		{"cancel with no reason", "/cancel", `{}`},
		{"cancel with free text", "/cancel", `{"reason_code":"changed my mind"}`},
		{"unknown field", "/cadence", `{"interval_days":30,"doses":2}`},
		{"malformed json", "/defer", `{"days":`},
		{"bad resume date", "/pause", `{"paused_until":"soon"}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newScheduleWithHorizon(t, repo)
			rec := do(t, h, http.MethodPost, "/schedules/"+s.ID+tc.path, tc.body, customerCred(t, s.CustomerID))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestTransitionsOnUnknownScheduleAreNotFound(t *testing.T) {
	h, _, _ := newAPI(t)
	missing := uuid.NewString()

	for path, body := range map[string]string{
		"/pause":   `{}`,
		"/resume":  ``,
		"/skip":    ``,
		"/defer":   `{"days":7}`,
		"/cadence": `{"interval_days":60}`,
		"/cancel":  `{"reason_code":"other"}`,
	} {
		rec := do(t, h, http.MethodPost, "/schedules/"+missing+path, body, customerCred(t, "cust_nobody"))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404; body = %s", path, rec.Code, rec.Body.String())
		}
	}
}

// GET must not perform a transition. Routing these as POST-only is what keeps a
// prefetching browser or a link crawler from canceling somebody's schedule.
func TestTransitionsRejectGet(t *testing.T) {
	h, repo, _ := newAPI(t)
	s := newScheduleWithHorizon(t, repo)

	for _, path := range []string{"/pause", "/resume", "/skip", "/defer", "/cadence", "/cancel"} {
		rec := do(t, h, http.MethodGet, "/schedules/"+s.ID+path, ``, customerCred(t, s.CustomerID))
		if rec.Code == http.StatusOK {
			t.Errorf("GET %s succeeded; transitions must not be reachable by GET", path)
		}
	}
	if got, err := repo.GetSchedule(context.Background(), s.ID, store.SystemScope()); err != nil {
		t.Fatalf("get: %v", err)
	} else if got.Status != domain.ScheduleActive {
		t.Errorf("status = %s after GET requests, want active", got.Status)
	}
}

// An unexpected failure is a 500 with a generic message — the internal error text
// never reaches the customer.
func TestUnexpectedErrorsAreInternalServerError(t *testing.T) {
	h := httpapi.TransitionHandler{Service: failingService{}}
	mux := http.NewServeMux()
	// Wired through the real middleware: without it the handler would answer 500
	// because no principal was attached, and this test would pass without ever
	// reaching the failing service it is supposed to be exercising.
	mux.Handle("POST /schedules/{id}/cancel", testMiddleware().RequireCustomer(http.HandlerFunc(h.Cancel)))

	rec := do(t, mux, http.MethodPost, "/schedules/abc/cancel", `{"reason_code":"other"}`, customerCred(t, "cust_anyone"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("internal error text leaked to the customer: %s", rec.Body.String())
	}
}

// failingService stands in for a repository that is down.
type failingService struct{}

var errDown = errors.New("dial tcp 127.0.0.1:5432: connection refused")

func (failingService) Pause(context.Context, string, *domain.Date, schedule.Caller) (domain.Schedule, error) {
	return domain.Schedule{}, errDown
}
func (failingService) Resume(context.Context, string, schedule.Caller) (domain.Schedule, error) {
	return domain.Schedule{}, errDown
}
func (failingService) SkipNext(context.Context, string, schedule.Caller) (domain.Schedule, error) {
	return domain.Schedule{}, errDown
}
func (failingService) Defer(context.Context, string, int, schedule.Caller) (domain.Schedule, error) {
	return domain.Schedule{}, errDown
}
func (failingService) ChangeCadence(context.Context, string, int, schedule.Caller) (domain.Schedule, error) {
	return domain.Schedule{}, errDown
}
func (failingService) Cancel(context.Context, string, string, schedule.Caller) (domain.Schedule, error) {
	return domain.Schedule{}, errDown
}
