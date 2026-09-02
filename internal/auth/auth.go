// Package auth establishes who is calling the service.
//
// Two kinds of caller reach CadenceOS, and they authenticate differently because they
// arrive from different places (spec §4). A customer acting in the portal arrives
// through the WP mu-plugin, which exchanges a WordPress nonce for a signed JWT; the
// portal's actions carry that token. Schedule creation, by contrast, happens
// server-side at checkout, where there is no browser session to mint a token from — so
// the WP backend presents a service credential instead.
//
// Nothing in this package trusts a customer identifier that arrived in a request body
// or a URL. The identity comes from the verified credential and nowhere else, which is
// the whole point: before this package existed, any caller who guessed a schedule UUID
// could read or mutate it.
package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"strings"

	"github.com/EPW80/replenishment-system/internal/domain"
)

// ErrUnauthenticated is returned for every failed verification.
//
// It is deliberately the single error for "bad credential", whatever the underlying
// cause — an expired token, a wrong signature and a malformed header are
// indistinguishable to the caller. Telling an attacker which part of their forgery was
// wrong is free help. Causes are wrapped for the service's own logs, never for the
// response body.
var ErrUnauthenticated = errors.New("unauthenticated")

// Kind is the sort of caller a Principal represents.
type Kind string

const (
	// KindCustomer is an end customer acting in the portal, identified by a verified
	// JWT the WP mu-plugin issued for them.
	KindCustomer Kind = "customer"

	// KindService is a trusted backend — today, WooCommerce checkout creating a
	// schedule. It is not tied to any one customer.
	KindService Kind = "service"
)

// Principal is a verified caller.
type Principal struct {
	Kind Kind

	// CustomerID is the WooCommerce customer this caller may act for. It is set only
	// for KindCustomer: a service credential vouches for whichever customer its
	// request names, so there is no single customer to bind it to.
	CustomerID string
}

// Actor records who caused a transition, for the append-only event log.
//
// A service credential maps to ActorSystem rather than ActorCustomer even though a
// customer's checkout is what set it off. The distinction is what CadenceOS can
// actually verify: it knows a trusted backend called, not that a person clicked. The
// same reasoning as release metadata's test_status — record the fact, not the claim.
func (p Principal) Actor() domain.EventActor {
	if p.Kind == KindCustomer {
		return domain.ActorCustomer
	}
	return domain.ActorSystem
}

// OwnsCustomer reports whether this caller may act for the given customer.
//
// A service credential may act for any customer; a customer may act only for
// themselves. Callers turn a false into a 404 rather than a 403 — see the handlers.
func (p Principal) OwnsCustomer(customerID string) bool {
	switch p.Kind {
	case KindService:
		return true
	case KindCustomer:
		return p.CustomerID != "" && p.CustomerID == customerID
	default:
		return false
	}
}

// ServiceKeyVerifier checks the shared credential presented by a trusted backend.
type ServiceKeyVerifier struct {
	// digest is the SHA-256 of the configured key rather than the key itself, so the
	// comparison below runs over two fixed-length values. A constant-time compare of
	// the raw strings would still reveal the key's length through its result for
	// mismatched lengths; hashing first removes even that.
	digest [sha256.Size]byte
}

// NewServiceKeyVerifier returns a verifier for the configured service credential.
func NewServiceKeyVerifier(key string) ServiceKeyVerifier {
	return ServiceKeyVerifier{digest: sha256.Sum256([]byte(key))}
}

// Verify checks a presented credential and returns the service principal.
func (v ServiceKeyVerifier) Verify(presented string) (Principal, error) {
	got := sha256.Sum256([]byte(presented))
	if subtle.ConstantTimeCompare(got[:], v.digest[:]) != 1 {
		return Principal{}, ErrUnauthenticated
	}
	return Principal{Kind: KindService}, nil
}

// ParseBearer pulls the credential out of an Authorization header value.
//
// The scheme is matched case-insensitively: RFC 6750 defines it as case-insensitive,
// and a client sending "bearer" is well-behaved rather than wrong.
func ParseBearer(header string) (string, error) {
	const scheme = "bearer "
	if len(header) < len(scheme) || !strings.EqualFold(header[:len(scheme)], scheme) {
		return "", ErrUnauthenticated
	}
	credential := strings.TrimSpace(header[len(scheme):])
	if credential == "" {
		return "", ErrUnauthenticated
	}
	return credential, nil
}

// principalKey types the context key so nothing else can collide with it.
type principalKey struct{}

// WithPrincipal returns a context carrying the verified caller.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// FromContext returns the verified caller placed by the auth middleware.
//
// The false return is not a routine case to paper over: a handler reaching it is one
// that was wired without its middleware, which is a bug in the router rather than a
// request to reject politely.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}
