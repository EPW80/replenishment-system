package httpapi_test

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/EPW80/replenishment-system/internal/domain"
	"github.com/EPW80/replenishment-system/internal/store"
)

// protectedRoutes is every route that must refuse an anonymous caller. Kept as one
// list so a route added without a credential shows up as a failure here rather than as
// an incident.
var protectedRoutes = []struct{ method, path, body string }{
	{http.MethodPost, "/schedules", `{"customer_id":"c","interval_days":30,"anchor_date":"2026-01-01","timezone":"UTC","items":[{"sku":"S","quantity":1}]}`},
	{http.MethodGet, "/schedules/{id}", ""},
	{http.MethodGet, "/schedules/{id}/occurrences", ""},
	{http.MethodGet, "/customers/{customer}/schedules", ""},
	{http.MethodPost, "/schedules/{id}/pause", `{}`},
	{http.MethodPost, "/schedules/{id}/resume", `{}`},
	{http.MethodPost, "/schedules/{id}/skip", `{"idempotency_key":"skip-key-1"}`},
	{http.MethodPost, "/schedules/{id}/defer", `{"days":7,"idempotency_key":"defer-key-1"}`},
	{http.MethodPost, "/schedules/{id}/cadence", `{"interval_days":45}`},
	{http.MethodPost, "/schedules/{id}/cancel", `{"reason_code":"other"}`},
}

// fill substitutes a real schedule and customer into a route template.
func fill(path string, s domain.Schedule) string {
	switch {
	case path == "/customers/{customer}/schedules":
		return "/customers/" + s.CustomerID + "/schedules"
	default:
		return replaceID(path, s.ID)
	}
}

func replaceID(path, id string) string {
	out := ""
	for i := 0; i < len(path); i++ {
		if len(path[i:]) >= 4 && path[i:i+4] == "{id}" {
			out += id
			i += 3
			continue
		}
		out += string(path[i])
	}
	return out
}

func TestUnauthenticatedRequestsAreRejected(t *testing.T) {
	h, repo, _ := newAPI(t)
	s := newScheduleWithHorizon(t, repo)

	for _, r := range protectedRoutes {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			rec := do(t, h, r.method, fill(r.path, s), r.body, "")
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
			// A 401 without this header is not a well-formed challenge.
			if rec.Header().Get("WWW-Authenticate") == "" {
				t.Error("no WWW-Authenticate header on the 401")
			}
		})
	}
}

func TestHealthzNeedsNoCredential(t *testing.T) {
	h, _, _ := newAPI(t)

	// The deploy probe reaches /healthz before any credential exists, and a health
	// check that needs a secret is a health check that reports outages as failures of
	// itself.
	if rec := do(t, h, http.MethodGet, "/healthz", "", ""); rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 without a credential", rec.Code)
	}
}

// Each credential works only on the routes it is for. Without this, a customer token
// stolen from a browser would be enough to create schedules.
func TestCredentialsAreNotInterchangeable(t *testing.T) {
	h, repo, _ := newAPI(t)
	s := newScheduleWithHorizon(t, repo)

	t.Run("a customer token cannot create a schedule", func(t *testing.T) {
		body := `{"customer_id":"` + s.CustomerID + `","interval_days":30,` +
			`"anchor_date":"2026-01-01","timezone":"UTC","items":[{"sku":"S","quantity":1}]}`
		rec := do(t, h, http.MethodPost, "/schedules", body, customerCred(t, s.CustomerID))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("the service key cannot act as a customer", func(t *testing.T) {
		rec := do(t, h, http.MethodGet, "/schedules/"+s.ID, "", serviceCred())
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rec.Code)
		}
	})
}

// The regression this whole change exists for: before it, any caller who guessed a
// schedule UUID could read and mutate it.
func TestOneCustomerCannotReachAnothersSchedule(t *testing.T) {
	h, repo, _ := newAPI(t)

	victim := newScheduleWithHorizon(t, repo)
	attacker := newScheduleWithHorizon(t, repo)
	if victim.CustomerID == attacker.CustomerID {
		t.Fatal("fixture produced one customer; the test would prove nothing")
	}
	cred := customerCred(t, attacker.CustomerID)

	// 404 rather than 403 throughout: a 403 would confirm the ID names a real
	// schedule, which is exactly what an attacker enumerating UUIDs wants to learn.
	t.Run("reads", func(t *testing.T) {
		for _, path := range []string{"/schedules/" + victim.ID, "/schedules/" + victim.ID + "/occurrences"} {
			rec := do(t, h, http.MethodGet, path, "", cred)
			if rec.Code != http.StatusNotFound {
				t.Errorf("GET %s = %d, want 404\nbody: %s", path, rec.Code, rec.Body.String())
			}
		}
	})

	t.Run("another customer's schedule list", func(t *testing.T) {
		rec := do(t, h, http.MethodGet, "/customers/"+victim.CustomerID+"/schedules", "", cred)
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404\nbody: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("transitions", func(t *testing.T) {
		for _, tc := range []struct{ path, body string }{
			{"/pause", `{}`},
			{"/resume", `{}`},
			{"/skip", `{"idempotency_key":"skip-key-1"}`},
			{"/defer", `{"days":7,"idempotency_key":"defer-key-1"}`},
			{"/cadence", `{"interval_days":45}`},
			{"/cancel", `{"reason_code":"other"}`},
		} {
			rec := do(t, h, http.MethodPost, "/schedules/"+victim.ID+tc.path, tc.body, cred)
			if rec.Code != http.StatusNotFound {
				t.Errorf("POST %s = %d, want 404\nbody: %s", tc.path, rec.Code, rec.Body.String())
			}
		}

		// A refused transition must also have changed nothing. A 404 that still
		// paused the schedule would be the worst of both.
		got, err := repo.GetSchedule(context.Background(), victim.ID, store.SystemScope())
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.Status != domain.ScheduleActive {
			t.Errorf("status = %s after refused transitions, want active", got.Status)
		}
		if got.IntervalDays != victim.IntervalDays {
			t.Errorf("interval_days = %d, want %d", got.IntervalDays, victim.IntervalDays)
		}
		occurrences, err := repo.ListOccurrences(context.Background(), victim.ID, store.SystemScope())
		if err != nil {
			t.Fatalf("list occurrences: %v", err)
		}
		for _, o := range occurrences {
			if o.Status != domain.OccurrencePlanned {
				t.Errorf("occurrence %d = %s, want planned", o.SequenceNo, o.Status)
			}
		}
	})
}

// A schedule that does not exist and one belonging to someone else must be
// indistinguishable, or the 404 leaks by timing of behaviour rather than status code.
func TestMissingAndForbiddenLookTheSame(t *testing.T) {
	h, repo, _ := newAPI(t)
	victim := newScheduleWithHorizon(t, repo)
	cred := customerCred(t, "cust_"+uuid.NewString()[:8])

	missing := do(t, h, http.MethodGet, "/schedules/"+uuid.NewString(), "", cred)
	forbidden := do(t, h, http.MethodGet, "/schedules/"+victim.ID, "", cred)

	if missing.Code != forbidden.Code {
		t.Errorf("missing = %d, another customer's = %d; they must match",
			missing.Code, forbidden.Code)
	}
	if missing.Body.String() != forbidden.Body.String() {
		t.Errorf("bodies differ:\nmissing:   %s\nforbidden: %s",
			missing.Body.String(), forbidden.Body.String())
	}
}

// The audit log records the verified caller rather than an assumption about them.
func TestTransitionRecordsTheVerifiedActor(t *testing.T) {
	h, repo, _ := newAPI(t)
	s := newScheduleWithHorizon(t, repo)

	rec := do(t, h, http.MethodPost, "/schedules/"+s.ID+"/pause", `{}`, customerCred(t, s.CustomerID))
	if rec.Code != http.StatusOK {
		t.Fatalf("pause = %d: %s", rec.Code, rec.Body.String())
	}

	events, err := repo.ListEvents(context.Background(), s.ID)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var paused *domain.ScheduleEvent
	for i := range events {
		if events[i].EventType == domain.EventSchedulePaused {
			paused = &events[i]
		}
	}
	if paused == nil {
		t.Fatal("no pause event recorded")
	}
	if paused.Actor != domain.ActorCustomer {
		t.Errorf("actor = %q, want customer", paused.Actor)
	}
}

// Contending transitions surface as 409 at the HTTP layer, not as two 200s.
//
// This is the customer-visible half of the locking work: the loser is told the
// schedule is no longer in a state that accepts the action, and a client that retries
// after a resume succeeds. Two 200s would mean the portal showed two people the same
// action succeeding while only one of them changed anything.
func TestConcurrentTransitionsYieldOneSuccessAndConflicts(t *testing.T) {
	h, repo, _ := newAPI(t)
	s := newScheduleWithHorizon(t, repo)
	cred := customerCred(t, s.CustomerID)

	const callers = 6
	codes := make([]int, callers)

	var start, done sync.WaitGroup
	start.Add(1)
	for i := 0; i < callers; i++ {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			codes[i] = do(t, h, http.MethodPost, "/schedules/"+s.ID+"/pause", `{}`, cred).Code
		}()
	}
	start.Done()
	done.Wait()

	var ok, conflict int
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
			conflict++
		default:
			t.Errorf("unexpected status %d", c)
		}
	}
	if ok != 1 {
		t.Errorf("%d requests got 200, want exactly 1", ok)
	}
	if conflict != callers-1 {
		t.Errorf("%d requests got 409, want %d", conflict, callers-1)
	}
}
