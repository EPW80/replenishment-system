package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/EPW80/replenishment-system/internal/auth"
)

// Middleware authenticates requests before they reach a handler.
//
// There are two entry points rather than one that sniffs the credential, because the
// two kinds of caller arrive on different routes (spec §4): a customer acts in the
// portal, a trusted backend creates schedules at checkout. Splitting them by route
// means no request is ever ambiguous about which credential it is presenting, and a
// stolen customer token cannot be replayed against the creation endpoint just because
// the server was willing to try it both ways.
type Middleware struct {
	Tokens     auth.TokenVerifier
	ServiceKey auth.ServiceKeyVerifier

	// Log records why a credential was rejected. The response never carries that
	// reason; the operator still needs it. nil uses the default logger.
	Log *slog.Logger
}

// RequireCustomer admits a request carrying a valid customer token.
func (m Middleware) RequireCustomer(next http.Handler) http.Handler {
	return m.authenticate(next, func(credential string) (auth.Principal, error) {
		return m.Tokens.Verify(credential)
	})
}

// RequireService admits a request carrying the service credential.
func (m Middleware) RequireService(next http.Handler) http.Handler {
	return m.authenticate(next, m.ServiceKey.Verify)
}

// authenticate is the shared shape of both middlewares: pull the bearer credential,
// verify it, attach the principal, or refuse.
func (m Middleware) authenticate(next http.Handler, verify func(string) (auth.Principal, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		credential, err := auth.ParseBearer(r.Header.Get("Authorization"))
		if err == nil {
			var principal auth.Principal
			principal, err = verify(credential)
			if err == nil {
				next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), principal)))
				return
			}
		}

		m.logger().InfoContext(r.Context(), "rejected credential",
			"method", r.Method, "path", r.URL.Path, "error", err)

		// WWW-Authenticate is what makes this a well-formed 401 rather than a bare
		// refusal, and the body says nothing about which part of the credential was
		// wrong — an attacker gets no help narrowing their next attempt.
		w.Header().Set("WWW-Authenticate", `Bearer realm="cadenceos"`)
		writeError(w, http.StatusUnauthorized, "valid credentials are required")
	})
}

func (m Middleware) logger() *slog.Logger {
	if m.Log != nil {
		return m.Log
	}
	return slog.Default()
}
