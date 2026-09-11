package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EPW80/replenishment-system/internal/httpapi"
)

func TestConsoleHandler(t *testing.T) {
	t.Parallel()

	h := httpapi.NewConsoleHandler()
	tests := []struct {
		path        string
		contentType string
		contains    string
	}{
		{path: "/console/", contentType: "text/html", contains: "CadenceOS Console"},
		{path: "/console/schedules/example", contentType: "text/html", contains: "CadenceOS Console"},
		{path: "/console/styles.css", contentType: "text/css", contains: "--cad-ground"},
		{path: "/console/app.js", contentType: "text/javascript", contains: "loadSchedules"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
			}
			if got := rr.Header().Get("Content-Type"); !strings.HasPrefix(got, tt.contentType) {
				t.Fatalf("Content-Type = %q, want prefix %q", got, tt.contentType)
			}
			if !strings.Contains(rr.Body.String(), tt.contains) {
				t.Fatalf("body does not contain %q", tt.contains)
			}
			if got := rr.Header().Get("Content-Security-Policy"); got == "" {
				t.Fatal("Content-Security-Policy is missing")
			}
		})
	}
}
