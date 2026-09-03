package notify

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPostmarkSenderSendsTheExpectedRequest(t *testing.T) {
	var gotReq postmarkRequest
	var gotToken string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Postmark-Server-Token")
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ErrorCode":0,"Message":"OK"}`))
	}))
	defer srv.Close()

	sender := NewPostmarkSender("test-api-key", "orders@example.com")
	sender.postmarkHost = srv.URL

	if err := sender.Send(t.Context(), "customer@example.com", "Subject line", "<p>body</p>"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotToken != "test-api-key" {
		t.Errorf("X-Postmark-Server-Token = %q, want %q", gotToken, "test-api-key")
	}
	if gotReq.From != "orders@example.com" || gotReq.To != "customer@example.com" ||
		gotReq.Subject != "Subject line" || gotReq.HTMLBody != "<p>body</p>" {
		t.Errorf("request = %+v", gotReq)
	}
}

func TestPostmarkSenderReturnsAnErrorOnRejection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"ErrorCode":300,"Message":"Invalid email address"}`))
	}))
	defer srv.Close()

	sender := NewPostmarkSender("test-api-key", "orders@example.com")
	sender.postmarkHost = srv.URL

	err := sender.Send(t.Context(), "not-an-address", "Subject", "<p>body</p>")
	if err == nil {
		t.Fatal("expected an error for a rejected send")
	}
}

func TestPostmarkSenderReturnsAnErrorOnTransportFailure(t *testing.T) {
	sender := NewPostmarkSender("test-api-key", "orders@example.com")
	sender.postmarkHost = "http://127.0.0.1:0" // nothing listens here

	if err := sender.Send(t.Context(), "customer@example.com", "Subject", "<p>body</p>"); err == nil {
		t.Fatal("expected an error when the request cannot be sent at all")
	}
}
