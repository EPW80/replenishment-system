package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAllowed covers the proxy's CSRF boundary.
//
// This is the one part of the portal work CI can protect. The widget's own checks run
// in a browser and nothing runs them automatically, but `allowed` is Go, so a
// regression here fails the build rather than waiting to be noticed.
//
// The hazard it guards was demonstrated on PR #61: the proxy attaches a minted customer
// credential to whatever it forwards, and the upstream does not inspect Content-Type
// (internal/httpapi.decodeJSON), so a POST with a text/plain body is a CORS-simple
// request needing no preflight. Any page a developer had open could cancel a real
// schedule. The reply is unreadable to the attacker; the write lands anyway.
func TestAllowed(t *testing.T) {
	const origin = "http://127.0.0.1:8081"

	tests := []struct {
		name    string
		method  string
		origin  string
		wantErr bool
	}{
		{name: "GET needs no origin", method: http.MethodGet, wantErr: false},
		{name: "HEAD needs no origin", method: http.MethodHead, wantErr: false},
		{name: "GET ignores a foreign origin", method: http.MethodGet, origin: "https://evil.example", wantErr: false},

		{name: "same-origin write", method: http.MethodPost, origin: origin, wantErr: false},
		{name: "cross-origin write", method: http.MethodPost, origin: "https://evil.example", wantErr: true},

		// A browser always sets Origin on a cross-origin POST, so a missing one has
		// nothing legitimate to be here. Allowing it would reopen the hole to any
		// client that simply omits the header.
		{name: "write with no origin", method: http.MethodPost, origin: "", wantErr: true},

		// A prefix or a scheme change is a different origin, not this one.
		{name: "origin differing by scheme", method: http.MethodPost, origin: "https://127.0.0.1:8081", wantErr: true},
		{name: "origin differing by port", method: http.MethodPost, origin: "http://127.0.0.1:9999", wantErr: true},
		{name: "origin with a trailing slash", method: http.MethodPost, origin: origin + "/", wantErr: true},

		// Every other write verb runs the same gate.
		{name: "PUT is a write", method: http.MethodPut, origin: "https://evil.example", wantErr: true},
		{name: "DELETE is a write", method: http.MethodDelete, origin: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, "/schedules/abc/cancel", nil)
			if tt.origin != "" {
				r.Header.Set("Origin", tt.origin)
			}

			err := allowed(r, origin)
			if tt.wantErr && err == nil {
				t.Fatalf("allowed(%s, origin=%q) = nil, want an error", tt.method, tt.origin)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("allowed(%s, origin=%q) = %v, want nil", tt.method, tt.origin, err)
			}
		})
	}
}

// TestRequireLoopback guards the other half of the same reasoning: this process mints
// customer credentials, so it must not be reachable from off-host.
func TestRequireLoopback(t *testing.T) {
	tests := []struct {
		addr    string
		wantErr bool
	}{
		{addr: "127.0.0.1:8081", wantErr: false},
		{addr: "localhost:8081", wantErr: false},
		{addr: "[::1]:8081", wantErr: false},
		{addr: "0.0.0.0:8081", wantErr: true},
		{addr: "192.168.1.10:8081", wantErr: true},
		{addr: ":8081", wantErr: true},
		{addr: "garbage", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			err := requireLoopback(tt.addr)
			if tt.wantErr && err == nil {
				t.Fatalf("requireLoopback(%q) = nil, want an error", tt.addr)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("requireLoopback(%q) = %v, want nil", tt.addr, err)
			}
		})
	}
}
