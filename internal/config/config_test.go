package config

import (
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	const validURL = "postgres://u:p@localhost:5432/db?sslmode=disable"
	const validSecret = "a-test-secret-of-at-least-32-characters"

	// setRequired supplies every variable Load insists on, so each subtest can vary
	// the one thing it is actually about. That is DATABASE_URL alone: the auth
	// credentials are RequireAuth's to check, and TestRequireAuth covers them.
	setRequired := func(t *testing.T) {
		t.Helper()
		t.Setenv("DATABASE_URL", validURL)
	}

	t.Run("requires DATABASE_URL", func(t *testing.T) {
		setRequired(t)
		t.Setenv("DATABASE_URL", "")
		if _, err := Load(); err == nil {
			t.Fatal("expected an error when DATABASE_URL is unset; a service that silently starts against the wrong database is worse than one that refuses to start")
		}
	})

	t.Run("applies defaults", func(t *testing.T) {
		setRequired(t)
		c, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if c.Port != 8080 {
			t.Errorf("Port = %d, want 8080", c.Port)
		}
		// Spec §5: default horizon is 3 planned occurrences ahead.
		if c.MaterializeHorizon != 3 {
			t.Errorf("MaterializeHorizon = %d, want 3", c.MaterializeHorizon)
		}
		if c.BuildSHA != "unknown" {
			t.Errorf("BuildSHA = %q, want %q", c.BuildSHA, "unknown")
		}
	})

	t.Run("rejects invalid values", func(t *testing.T) {
		for _, tc := range []struct{ name, key, value string }{
			{"non-numeric port", "PORT", "http"},
			{"port out of range", "PORT", "70000"},
			{"port zero", "PORT", "0"},
			{"non-numeric horizon", "MATERIALIZE_HORIZON", "many"},
			{"horizon below one", "MATERIALIZE_HORIZON", "0"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				setRequired(t)
				t.Setenv(tc.key, tc.value)
				if _, err := Load(); err == nil {
					t.Errorf("expected an error for %s=%q", tc.key, tc.value)
				}
			})
		}
	})

	// cmd/materialize and cmd/sweep authenticate nobody, so Load must not demand an
	// HS256 signing key from a job that only tops up an occurrence horizon. The
	// service still refuses to boot without them -- see TestRequireAuth.
	t.Run("does not require the auth credentials", func(t *testing.T) {
		setRequired(t)
		t.Setenv("PORTAL_JWT_SECRET", "")
		t.Setenv("SERVICE_API_KEY", "")
		if _, err := Load(); err != nil {
			t.Fatalf("Load without the auth credentials: %v", err)
		}
	})

	t.Run("still reads the auth credentials when present", func(t *testing.T) {
		setRequired(t)
		t.Setenv("PORTAL_JWT_SECRET", validSecret)
		t.Setenv("SERVICE_API_KEY", validSecret)
		c, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if c.PortalJWTSecret != validSecret || c.ServiceAPIKey != validSecret {
			t.Error("Load did not carry the auth credentials through to the Config")
		}
	})

	t.Run("defaults the token issuer and audience", func(t *testing.T) {
		setRequired(t)
		c, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if c.PortalJWTIssuer != "cadenceos-portal" || c.PortalJWTAudience != "cadenceos" {
			t.Errorf("issuer/audience = %q/%q", c.PortalJWTIssuer, c.PortalJWTAudience)
		}
	})

	t.Run("reads overrides", func(t *testing.T) {
		setRequired(t)
		t.Setenv("PORT", "9090")
		t.Setenv("BUILD_SHA", "abc123")
		t.Setenv("MATERIALIZE_HORIZON", "5")
		c, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if c.Port != 9090 || c.BuildSHA != "abc123" || c.MaterializeHorizon != 5 {
			t.Errorf("overrides not applied: %+v", c)
		}
	})

	// cmd/cadenceos, cmd/materialize and cmd/sweep all call Load and none of them
	// sends email -- requiring Postmark config across the board would force every
	// deployment to configure it just to top up an occurrence horizon. Only
	// cmd/notify needs it, via RequireNotifications below.
	t.Run("does not require the notification fields", func(t *testing.T) {
		setRequired(t)
		if _, err := Load(); err != nil {
			t.Fatalf("Load without any POSTMARK_*/NOTIFICATION_* set: %v", err)
		}
	})
}

func TestRequireAuth(t *testing.T) {
	const validSecret = "a-test-secret-of-at-least-32-characters"

	valid := Config{PortalJWTSecret: validSecret, ServiceAPIKey: validSecret}

	if err := valid.RequireAuth(); err != nil {
		t.Fatalf("RequireAuth on a complete config: %v", err)
	}

	// The check moved out of Load; it did not go away. A service that starts without
	// these serves every schedule to anyone who asks, so each credential is checked
	// missing and too-short, exactly as Load used to.
	for _, key := range []string{"PORTAL_JWT_SECRET", "SERVICE_API_KEY"} {
		// withSecret returns a config whose named credential is set to value and
		// whose other credential is valid.
		withSecret := func(value string) Config {
			c := valid
			if key == "PORTAL_JWT_SECRET" {
				c.PortalJWTSecret = value
			} else {
				c.ServiceAPIKey = value
			}
			return c
		}

		t.Run(key+" missing", func(t *testing.T) {
			if err := withSecret("").RequireAuth(); err == nil {
				t.Errorf("expected an error when %s is unset", key)
			}
		})
		t.Run(key+" too short", func(t *testing.T) {
			if err := withSecret("short").RequireAuth(); err == nil {
				t.Errorf("expected an error when %s is below the minimum length", key)
			}
		})
		t.Run(key+" value never appears in the error", func(t *testing.T) {
			err := withSecret("tooshort-but-secret").RequireAuth()
			if err == nil {
				t.Fatalf("expected an error for a short %s", key)
			}
			if strings.Contains(err.Error(), "tooshort-but-secret") {
				t.Errorf("%s value leaked into the error: %v", key, err)
			}
		})
	}
}

func TestRequireNotifications(t *testing.T) {
	valid := Config{
		PostmarkAPIKey:             "11111111-2222-3333-4444-555555555555",
		NotificationFromAddress:    "orders@example.com",
		NotificationSupportContact: "support@example.com",
	}

	if err := valid.RequireNotifications(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	for _, tc := range []struct {
		name  string
		patch func(*Config)
	}{
		{"missing api key", func(c *Config) { c.PostmarkAPIKey = "" }},
		{"missing from address", func(c *Config) { c.NotificationFromAddress = "" }},
		{"malformed from address", func(c *Config) { c.NotificationFromAddress = "not-an-address" }},
		{"missing support contact", func(c *Config) { c.NotificationSupportContact = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := valid
			tc.patch(&c)
			if err := c.RequireNotifications(); err == nil {
				t.Error("expected an error")
			}
		})
	}
}
