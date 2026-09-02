package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// signingMethod is the one algorithm this service accepts.
//
// Pinning it is what makes the verifier safe rather than merely functional. A parser
// that accepts whatever the token's own header asks for is the classic JWT failure:
// "alg": "none" strips verification entirely, and an RS256 header against an HMAC
// verifier turns the public key into a signing key. The token does not get a vote on
// how it is checked.
const signingMethod = "HS256"

// leeway absorbs clock drift between the WP host minting tokens and this service.
// Small on purpose: it widens the window an expired token stays usable.
const leeway = 30 * time.Second

// TokenVerifier verifies customer tokens issued by the WP mu-plugin (spec §4).
type TokenVerifier struct {
	parser *jwt.Parser
	secret []byte
}

// TokenConfig is what a verifier needs to check a token.
type TokenConfig struct {
	Secret   string
	Issuer   string
	Audience string

	// Now is injected so tests can place a token in time rather than sleep. nil means
	// time.Now.
	Now func() time.Time
}

// NewTokenVerifier returns a verifier for the configured signing secret.
func NewTokenVerifier(cfg TokenConfig) TokenVerifier {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}

	opts := []jwt.ParserOption{
		jwt.WithValidMethods([]string{signingMethod}),
		jwt.WithIssuer(cfg.Issuer),
		jwt.WithAudience(cfg.Audience),
		// A token without an expiry never stops being valid, and one leaked from a
		// browser would stay useful indefinitely. Require the claim rather than
		// treating its absence as "no deadline".
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(leeway),
		jwt.WithTimeFunc(now),
	}

	return TokenVerifier{
		parser: jwt.NewParser(opts...),
		secret: []byte(cfg.Secret),
	}
}

// Verify checks a customer token and returns the customer it authenticates.
//
// Every failure returns ErrUnauthenticated with the cause wrapped for logs: the caller
// learns only that the credential was rejected.
func (v TokenVerifier) Verify(token string) (Principal, error) {
	var claims jwt.RegisteredClaims
	parsed, err := v.parser.ParseWithClaims(token, &claims, func(*jwt.Token) (any, error) {
		return v.secret, nil
	})
	if err != nil {
		return Principal{}, fmt.Errorf("%w: %v", ErrUnauthenticated, err)
	}
	if !parsed.Valid {
		return Principal{}, ErrUnauthenticated
	}

	// The subject carries the WooCommerce customer ID. A token that authenticates
	// nobody in particular cannot be allowed to act on a customer's schedules, so an
	// empty subject is a rejection rather than an anonymous session.
	if claims.Subject == "" {
		return Principal{}, fmt.Errorf("%w: token has no subject", ErrUnauthenticated)
	}

	return Principal{Kind: KindCustomer, CustomerID: claims.Subject}, nil
}
