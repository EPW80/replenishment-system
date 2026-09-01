// Package config loads service configuration from the environment.
//
// Everything the service needs to differ between deployments lives here. Spec §12
// requires that standing up a second brand be a configuration change and nothing
// more, so a value that varies per deployment belongs in this struct rather than in
// a constant somewhere in the code.
package config

import (
	"fmt"
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

	// PostmarkServerToken, EmailFromAddress and EmailSupportAddress configure
	// internal/notify (Phase 4). Only cmd/notify needs these, so Load does not
	// require them — a service or job that never sends email should not have to
	// set Postmark credentials to start. cmd/notify validates them itself, the
	// same way cmd/migrate validates DATABASE_URL itself rather than through this
	// shared loader.
	PostmarkServerToken string
	EmailFromAddress    string
	EmailSupportAddress string
}

// Load reads configuration from the environment, applying defaults.
//
// It returns an error rather than falling back to a default for DatabaseURL: a
// service that silently starts against the wrong database is worse than one that
// refuses to start.
func Load() (Config, error) {
	c := Config{
		DatabaseURL:         os.Getenv("DATABASE_URL"),
		Port:                8080,
		BuildSHA:            envOr("BUILD_SHA", "unknown"),
		MaterializeHorizon:  3,
		PostmarkServerToken: os.Getenv("POSTMARK_SERVER_TOKEN"),
		EmailFromAddress:    os.Getenv("EMAIL_FROM_ADDRESS"),
		EmailSupportAddress: os.Getenv("EMAIL_SUPPORT_ADDRESS"),
	}

	if c.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
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

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
