package notify_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/EPW80/replenishment-system/internal/notify"
)

func newTestSender(t *testing.T, handler http.HandlerFunc) *notify.PostmarkSender {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &notify.PostmarkSender{
		ServerToken: "test-token",
		FromAddress: "notices@cadenceos.example",
		HTTPClient:  srv.Client(),
		Endpoint:    srv.URL,
	}
}

func TestPostmarkSenderRequestShape(t *testing.T) {
	var gotToken, gotFrom, gotTo, gotSubject, gotHTML, gotText string

	sender := newTestSender(t, func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Postmark-Server-Token")
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		gotFrom, _ = body["From"].(string)
		gotTo, _ = body["To"].(string)
		gotSubject, _ = body["Subject"].(string)
		gotHTML, _ = body["HtmlBody"].(string)
		gotText, _ = body["TextBody"].(string)
		if body["MessageStream"] != "outbound" {
			t.Errorf("MessageStream = %v, want outbound", body["MessageStream"])
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"ErrorCode": 0, "Message": "OK", "MessageID": "abc"})
	})

	msg := notify.Message{
		To:       "customer@example.com",
		Subject:  "Your schedule is confirmed",
		HTMLBody: "<p>hello</p>",
		TextBody: "hello",
	}
	if err := sender.Send(context.Background(), msg); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if gotToken != "test-token" {
		t.Errorf("X-Postmark-Server-Token = %q, want %q", gotToken, "test-token")
	}
	if gotFrom != "notices@cadenceos.example" {
		t.Errorf("From = %q", gotFrom)
	}
	if gotTo != "customer@example.com" {
		t.Errorf("To = %q", gotTo)
	}
	if gotSubject != "Your schedule is confirmed" {
		t.Errorf("Subject = %q", gotSubject)
	}
	if gotHTML != "<p>hello</p>" || gotText != "hello" {
		t.Errorf("bodies not sent correctly: html=%q text=%q", gotHTML, gotText)
	}
}

func TestPostmarkSenderNonOKHTTPStatusIsAnError(t *testing.T) {
	sender := newTestSender(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"ErrorCode": 10, "Message": "Invalid server token"})
	})

	err := sender.Send(context.Background(), notify.Message{To: "c@example.com", Subject: "s"})
	if err == nil {
		t.Fatal("expected an error for a 401 response")
	}
}

// Postmark can return HTTP 200 with a non-zero ErrorCode — that is still a failure
// to deliver and must not be treated as success.
func TestPostmarkSenderNonZeroErrorCodeIsAnError(t *testing.T) {
	sender := newTestSender(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"ErrorCode": 300, "Message": "Invalid email request"})
	})

	err := sender.Send(context.Background(), notify.Message{To: "not-valid", Subject: "s"})
	if err == nil {
		t.Fatal("expected an error for a non-zero Postmark ErrorCode despite HTTP 200")
	}
}
