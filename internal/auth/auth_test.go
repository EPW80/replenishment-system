package auth_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/EPW80/replenishment-system/internal/auth"
	"github.com/EPW80/replenishment-system/internal/domain"
)

const (
	secret   = "portal-test-secret-at-least-32-characters"
	issuer   = "cadenceos-portal"
	audience = "cadenceos"
)

func verifier(now func() time.Time) auth.TokenVerifier {
	return auth.NewTokenVerifier(auth.TokenConfig{
		Secret: secret, Issuer: issuer, Audience: audience, Now: now,
	})
}

// claims returns a token that should verify, so each test below can change exactly one
// thing and attribute the rejection to it.
func claims() jwt.RegisteredClaims {
	return jwt.RegisteredClaims{
		Subject:   "cust_123",
		Issuer:    issuer,
		Audience:  jwt.ClaimStrings{audience},
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}
}

func sign(t *testing.T, c jwt.RegisteredClaims, key string) string {
	t.Helper()
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString([]byte(key))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signed
}

func TestVerifyAcceptsAWellFormedToken(t *testing.T) {
	p, err := verifier(nil).Verify(sign(t, claims(), secret))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if p.Kind != auth.KindCustomer {
		t.Errorf("Kind = %q, want customer", p.Kind)
	}
	if p.CustomerID != "cust_123" {
		t.Errorf("CustomerID = %q, want cust_123", p.CustomerID)
	}
}

func TestVerifyRejects(t *testing.T) {
	expired := claims()
	expired.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-2 * time.Hour))

	future := claims()
	future.NotBefore = jwt.NewNumericDate(time.Now().Add(2 * time.Hour))

	wrongIssuer := claims()
	wrongIssuer.Issuer = "somebody-else"

	wrongAudience := claims()
	wrongAudience.Audience = jwt.ClaimStrings{"another-brand"}

	noSubject := claims()
	noSubject.Subject = ""

	noExpiry := claims()
	noExpiry.ExpiresAt = nil

	for _, tc := range []struct {
		name  string
		token string
	}{
		// A token signed with a different key is the base case: without this the
		// verifier is decoration.
		{"a different signing key", sign(t, claims(), "some-other-secret-of-sufficient-length")},
		{"an expired token", sign(t, expired, secret)},
		{"a token that is not valid yet", sign(t, future, secret)},
		{"another issuer", sign(t, wrongIssuer, secret)},
		// Another brand's deployment shares this service's shape but not its
		// audience, so its tokens must not work here (spec §12).
		{"another audience", sign(t, wrongAudience, secret)},
		{"no subject to act for", sign(t, noSubject, secret)},
		// A token with no expiry never stops working, which is what makes a leaked
		// one permanent.
		{"no expiry", sign(t, noExpiry, secret)},
		{"a malformed token", "not.a.jwt"},
		{"an empty token", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := verifier(nil).Verify(tc.token); !errors.Is(err, auth.ErrUnauthenticated) {
				t.Errorf("Verify accepted %s (err = %v)", tc.name, err)
			}
		})
	}
}

// The two classic JWT forgeries. Both work against a parser that trusts the token's own
// header to say how it should be checked, which is why the accepted algorithm is
// pinned rather than read from the token.
func TestVerifyRejectsAlgorithmSubstitution(t *testing.T) {
	t.Run("alg none strips the signature entirely", func(t *testing.T) {
		token, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims()).
			SignedString(jwt.UnsafeAllowNoneSignatureType)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		if _, err := verifier(nil).Verify(token); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Errorf("Verify accepted an unsigned token (err = %v)", err)
		}
	})

	t.Run("RS256 header against an HMAC verifier", func(t *testing.T) {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatalf("generate key: %v", err)
		}
		token, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims()).SignedString(key)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		if _, err := verifier(nil).Verify(token); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Errorf("Verify accepted an RS256 token (err = %v)", err)
		}
	})
}

// Clock skew between the WP host and this service should not reject a token that only
// just expired, but the allowance is small and bounded.
func TestVerifyAllowsSmallClockSkew(t *testing.T) {
	c := claims()
	c.ExpiresAt = jwt.NewNumericDate(time.Now())
	token := sign(t, c, secret)

	justAfter := func() time.Time { return time.Now().Add(10 * time.Second) }
	if _, err := verifier(justAfter).Verify(token); err != nil {
		t.Errorf("Verify rejected a token inside the leeway: %v", err)
	}

	wellAfter := func() time.Time { return time.Now().Add(10 * time.Minute) }
	if _, err := verifier(wellAfter).Verify(token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("Verify accepted a token well past the leeway (err = %v)", err)
	}
}

// The rejection reason never reaches the caller — telling an attacker which part of
// their forgery was wrong is free help.
func TestVerifyErrorsAreAllTheSameSentinel(t *testing.T) {
	expired := claims()
	expired.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Hour))

	for _, token := range []string{
		sign(t, claims(), "some-other-secret-of-sufficient-length"),
		sign(t, expired, secret),
		"garbage",
	} {
		if _, err := verifier(nil).Verify(token); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Errorf("error is not ErrUnauthenticated: %v", err)
		}
	}
}

func TestServiceKeyVerifier(t *testing.T) {
	const key = "service-test-key-at-least-32-characters!!"
	v := auth.NewServiceKeyVerifier(key)

	p, err := v.Verify(key)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if p.Kind != auth.KindService {
		t.Errorf("Kind = %q, want service", p.Kind)
	}
	if p.CustomerID != "" {
		t.Errorf("CustomerID = %q, want empty — a service credential is not one customer", p.CustomerID)
	}

	for _, tc := range []struct{ name, presented string }{
		{"a different key", "service-test-key-at-least-32-characters??"},
		{"a prefix of the key", key[:20]},
		{"the empty string", ""},
		{"a longer string starting with the key", key + "extra"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := v.Verify(tc.presented); !errors.Is(err, auth.ErrUnauthenticated) {
				t.Errorf("Verify accepted %s", tc.name)
			}
		})
	}
}

func TestParseBearer(t *testing.T) {
	for _, tc := range []struct {
		name, header, want string
		wantErr            bool
	}{
		{name: "a bearer credential", header: "Bearer abc123", want: "abc123"},
		// RFC 6750 makes the scheme case-insensitive; a client sending "bearer" is
		// well-behaved rather than wrong.
		{name: "a lowercase scheme", header: "bearer abc123", want: "abc123"},
		{name: "surrounding space", header: "Bearer   abc123  ", want: "abc123"},
		{name: "no header at all", header: "", wantErr: true},
		{name: "another scheme", header: "Basic abc123", wantErr: true},
		{name: "the scheme with no credential", header: "Bearer ", wantErr: true},
		{name: "a bare credential", header: "abc123", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := auth.ParseBearer(tc.header)
			if tc.wantErr {
				if !errors.Is(err, auth.ErrUnauthenticated) {
					t.Errorf("ParseBearer(%q) = %q, %v; want an error", tc.header, got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Errorf("ParseBearer(%q) = %q, %v; want %q", tc.header, got, err, tc.want)
			}
		})
	}
}

// The actor recorded in the audit log is what the service can verify, not what it
// assumes: a service credential says a trusted backend called, not that a person did.
func TestPrincipalActor(t *testing.T) {
	if got := (auth.Principal{Kind: auth.KindCustomer}).Actor(); got != domain.ActorCustomer {
		t.Errorf("customer actor = %q, want %q", got, domain.ActorCustomer)
	}
	if got := (auth.Principal{Kind: auth.KindService}).Actor(); got != domain.ActorSystem {
		t.Errorf("service actor = %q, want %q", got, domain.ActorSystem)
	}
}

func TestPrincipalOwnsCustomer(t *testing.T) {
	customer := auth.Principal{Kind: auth.KindCustomer, CustomerID: "cust_a"}
	if !customer.OwnsCustomer("cust_a") {
		t.Error("a customer must own themselves")
	}
	if customer.OwnsCustomer("cust_b") {
		t.Error("a customer must not own another customer")
	}
	// An unset CustomerID must not match an unset path value and quietly authorize.
	if (auth.Principal{Kind: auth.KindCustomer}).OwnsCustomer("") {
		t.Error("an empty customer must not own the empty customer")
	}
	if !(auth.Principal{Kind: auth.KindService}).OwnsCustomer("cust_anything") {
		t.Error("a service credential acts for whichever customer its request names")
	}
	if (auth.Principal{}).OwnsCustomer("cust_a") {
		t.Error("a zero principal must own nothing")
	}
}

func TestContextRoundTrip(t *testing.T) {
	want := auth.Principal{Kind: auth.KindCustomer, CustomerID: "cust_a"}
	got, ok := auth.FromContext(auth.WithPrincipal(context.Background(), want))
	if !ok || got != want {
		t.Errorf("FromContext = %+v, %v; want %+v", got, ok, want)
	}
	if _, ok := auth.FromContext(context.Background()); ok {
		t.Error("a context with no principal must not report one")
	}
}
