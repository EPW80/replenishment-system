// Package config loads service configuration from the environment.
//
// Everything the service needs to differ between deployments lives here. Spec §12
// requires that standing up a second brand be a configuration change and nothing
// more, so a value that varies per deployment belongs in this struct rather than in
// a constant somewhere in the code.
package config

import (
	"fmt"
	"net/mail"
	"os"
	"strconv"
)

// Config is the service's runtime configuration.
type Config struct {
	// DatabaseURL is the Postgres connection string. Required.
	DatabaseURL string

	// Port is the HTTP listen port.
	Port int

	// BuildSHA is the commit this binary was built from, injected at deploy time.
	// The health check asserts on it: a deploy that silently kept serving the
	// previous version reports the previous SHA, which a plain 200 cannot reveal.
	BuildSHA string

	// MaterializeHorizon is how many future planned occurrences to keep per active
	// schedule. Spec §5 default is 3.
	MaterializeHorizon int

	// PortalJWTSecret is the HS256 key the WP mu-plugin signs customer tokens with
	// (spec §4). Required.
	PortalJWTSecret string

	// PortalJWTIssuer and PortalJWTAudience are the iss and aud claims a customer
	// token must carry. Checking them is what stops a token minted for some other
	// service, or by some other brand's deployment, from working here.
	PortalJWTIssuer   string
	PortalJWTAudience string

	// ServiceAPIKey is the credential a trusted backend presents to create schedules
	// at checkout, where there is no customer session to mint a token from. Required.
	ServiceAPIKey string

	// PostmarkAPIKey, NotificationFromAddress and NotificationSupportContact
	// (spec §7, Phase 4) are read here but validated only by cmd/notify, not by
	// Load itself: cmd/cadenceos, cmd/materialize, cmd/sweep and cmd/migrate all
	// call Load and none of them sends email, so requiring these across the board
	// would force every deployment to configure Postmark just to run a migration.
	PostmarkAPIKey             string
	NotificationFromAddress    string
	NotificationSupportContact string
}

// minSecretLength is the shortest credential this service will start with.
//
// 32 bytes matches HS256's output size: a shorter key does not make the signature
// weaker in an obvious way, which is exactly why a short one gets used by accident and
// stays. Rejecting it at boot is cheaper than discovering it in a review.
const minSecretLength = 32

// Load reads configuration from the environment, applying defaults.
//
// It returns an error rather than falling back to a default for DatabaseURL: a
// service that silently starts against the wrong database is worse than one that
// refuses to start.
func Load() (Config, error) {
	c := Config{
		DatabaseURL:        os.Getenv("DATABASE_URL"),
		Port:               8080,
		BuildSHA:           envOr("BUILD_SHA", "unknown"),
		MaterializeHorizon: 3,
		PortalJWTSecret:    os.Getenv("PORTAL_JWT_SECRET"),
		PortalJWTIssuer:    envOr("PORTAL_JWT_ISSUER", "cadenceos-portal"),
		PortalJWTAudience:  envOr("PORTAL_JWT_AUDIENCE", "cadenceos"),
		ServiceAPIKey:      os.Getenv("SERVICE_API_KEY"),

		PostmarkAPIKey:             os.Getenv("POSTMARK_API_KEY"),
		NotificationFromAddress:    os.Getenv("NOTIFICATION_FROM_ADDRESS"),
		NotificationSupportContact: os.Getenv("NOTIFICATION_SUPPORT_CONTACT"),
	}

	if c.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}

	// The credentials are required rather than defaulted for the same reason as
	// DATABASE_URL, one step further: a service that starts without them serves every
	// schedule to anyone who asks. Refusing to boot is the only safe default.
	if err := requireSecret("PORTAL_JWT_SECRET", c.PortalJWTSecret); err != nil {
		return Config{}, err
	}
	if err := requireSecret("SERVICE_API_KEY", c.ServiceAPIKey); err != nil {
		return Config{}, err
	}

	if v := os.Getenv("PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			return Config{}, fmt.Errorf("PORT must be a number, got %q", v)
		}
		if p < 1 || p > 65535 {
			return Config{}, fmt.Errorf("PORT out of range: %d", p)
		}
		c.Port = p
	}

	if v := os.Getenv("MATERIALIZE_HORIZON"); v != "" {
		h, err := strconv.Atoi(v)
		if err != nil {
			return Config{}, fmt.Errorf("MATERIALIZE_HORIZON must be a number, got %q", v)
		}
		if h < 1 {
			return Config{}, fmt.Errorf("MATERIALIZE_HORIZON must be at least 1, got %d", h)
		}
		c.MaterializeHorizon = h
	}

	return c, nil
}

// requireSecret rejects a missing or too-short credential.
//
// The error names the variable but never its value: a config error is one of the
// likelier things to end up pasted into an issue or a log aggregator.
func requireSecret(key, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", key)
	}
	if len(value) < minSecretLength {
		return fmt.Errorf("%s must be at least %d characters", key, minSecretLength)
	}
	return nil
}

// RequireNotifications validates the Phase 4 fields Load leaves unchecked.
// cmd/notify calls this itself, since it is the only binary that needs them.
//
// PostmarkAPIKey is checked for non-empty only, not via requireSecret: Postmark
// server tokens are UUIDs, so the 32-character minimum would pass coincidentally
// rather than meaningfully, the way it does for an HS256 key.
func (c Config) RequireNotifications() error {
	if c.PostmarkAPIKey == "" {
		return fmt.Errorf("POSTMARK_API_KEY is required")
	}
	if _, err := mail.ParseAddress(c.NotificationFromAddress); err != nil {
		return fmt.Errorf("NOTIFICATION_FROM_ADDRESS is not a valid address: %w", err)
	}
	if c.NotificationSupportContact == "" {
		return fmt.Errorf("NOTIFICATION_SUPPORT_CONTACT is required")
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
