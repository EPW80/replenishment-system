// Command portaldev serves the portal widget for local development.
//
// IT IS NOT DEPLOYED, and must not be. In production the browser never talks to
// CadenceOS directly: spec §4 puts a thin WordPress mu-plugin in front of it that
// exchanges a WP nonce for a portal JWT and proxies authenticated calls. This command
// is that mu-plugin's stand-in, so the widget can be developed against the real API
// without CadenceOS growing CORS middleware for a path production never takes.
//
// It mints portal credentials from PORTAL_JWT_SECRET, which is exactly the capability
// the mu-plugin holds and nothing else should. It therefore binds to loopback only and
// refuses to start on any other address. Nothing in Dockerfile or scripts/ references
// it, and nothing should.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	tokenTTL = time.Hour
	// Matches minSecretLength in internal/config.
	minSecretLength = 32
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8081", "loopback address to listen on")
	upstream := flag.String("upstream", "http://127.0.0.1:8080", "cadenceos base URL")
	dir := flag.String("dir", "web/portal", "directory holding the widget")
	customer := flag.String("customer", "", "customer id to mint the portal token for (required)")
	schedule := flag.String("schedule", "", "schedule id to render (required)")
	flag.Parse()

	if *customer == "" || *schedule == "" {
		log.Fatal("portaldev: -customer and -schedule are both required")
	}

	// The same floor cadenceos enforces in config.RequireAuth. Accepting a shorter one
	// here would start cleanly and then have the upstream reject every proxied request
	// as unauthenticated, which reads as a broken portal rather than a short secret.
	secret := os.Getenv("PORTAL_JWT_SECRET")
	if len(secret) < minSecretLength {
		log.Fatalf("portaldev: PORTAL_JWT_SECRET must be at least %d characters", minSecretLength)
	}

	if err := requireLoopback(*addr); err != nil {
		log.Fatalf("portaldev: %v", err)
	}

	target, err := url.Parse(*upstream)
	if err != nil {
		log.Fatalf("portaldev: parse upstream: %v", err)
	}

	minter := &minter{
		secret:   []byte(secret),
		customer: *customer,
		issuer:   envOr("PORTAL_JWT_ISSUER", "cadenceos-portal"),
		audience: envOr("PORTAL_JWT_AUDIENCE", "cadenceos"),
	}

	mux := http.NewServeMux()
	mux.Handle("/api/", http.StripPrefix("/api", proxy(target, "http://"+*addr, minter)))
	mux.Handle("/", widget(*dir, *schedule))

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("portaldev: http://%s (schedule %s, customer %s) -> %s", *addr, *schedule, *customer, target)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("portaldev: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// requireLoopback refuses any bind address that is reachable from off-host. This
// process mints customer credentials; exposing it on a LAN would hand them out.
func requireLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("parse -addr: %w", err)
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("-addr %q is not a loopback address; portaldev mints credentials and must not be reachable off-host", addr)
	}
	return nil
}

// minter issues the portal JWT the mu-plugin would issue after a successful nonce
// exchange: HS256, this customer as the subject, matching issuer and audience.
type minter struct {
	secret   []byte
	customer string
	issuer   string
	audience string
}

func (m *minter) token() (string, error) {
	return jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject:   m.customer,
		Issuer:    m.issuer,
		Audience:  jwt.ClaimStrings{m.audience},
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(tokenTTL)),
	}).SignedString(m.secret)
}

// allowed decides whether a request may carry a minted customer credential upstream.
//
// The hazard this guards is real and was demonstrated on PR #61: the proxy attaches a
// valid credential to whatever it forwards, and the upstream does not inspect
// Content-Type (internal/httpapi.decode), so a POST with a text/plain body is a
// CORS-simple request. Any page a developer has open could send one cross-origin, with
// no preflight to stop it, and cancel a real schedule. The attacker cannot read the
// reply, but the write has already happened.
//
// Reads are unrestricted. A write must carry an Origin header exactly matching this
// server's own, which a browser sets on every cross-origin POST and cannot be forged by
// page script. The header is required rather than merely checked when present: the
// cross-origin form always sends one, so a missing Origin has nothing legitimate to be
// in this context and allowing it would reopen the hole for any client that omits it.
func allowed(r *http.Request, origin string) error {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return nil
	}
	switch r.Header.Get("Origin") {
	case origin:
		return nil
	case "":
		return errors.New("portaldev requires an Origin header on writes")
	default:
		return errors.New("portaldev rejects cross-origin writes")
	}
}

func proxy(target *url.URL, origin string, m *minter) http.Handler {
	rp := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Host = target.Host
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			log.Printf("portaldev: upstream: %v", err)
			http.Error(w, `{"error":"upstream unreachable"}`, http.StatusBadGateway)
		},
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := allowed(r, origin); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusForbidden)
			return
		}

		token, err := m.token()
		if err != nil {
			log.Printf("portaldev: mint token: %v", err)
			http.Error(w, `{"error":"could not mint a portal token"}`, http.StatusInternalServerError)
			return
		}
		// The browser never sees this header; the real mu-plugin attaches it the same
		// way, server-side, which is why no token ever reaches client JavaScript.
		r.Header.Set("Authorization", "Bearer "+token)
		rp.ServeHTTP(w, r)
	})
}

// widget serves the static files, substituting the schedule id into the shell the way
// the WordPress theme would print it for the logged-in customer.
func widget(dir, scheduleID string) http.Handler {
	files := http.FileServer(http.Dir(dir))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/index.html" {
			files.ServeHTTP(w, r)
			return
		}

		shell, err := os.ReadFile(filepath.Join(dir, "index.html"))
		if err != nil {
			log.Printf("portaldev: read shell: %v", err)
			http.Error(w, "portal shell not found", http.StatusInternalServerError)
			return
		}

		page := strings.Replace(
			string(shell),
			`data-schedule-id=""`,
			fmt.Sprintf("data-schedule-id=%q", scheduleID),
			1,
		)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprint(w, page)
	})
}
