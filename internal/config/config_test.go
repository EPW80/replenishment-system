package config

import (
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	const validURL = "postgres://u:p@localhost:5432/db?sslmode=disable"
	const validSecret = "a-test-secret-of-at-least-32-characters"

	// setRequired supplies every variable Load insists on, so each subtest can vary
	// the one thing it is actually about.
	setRequired := func(t *testing.T) {
		t.Helper()
		t.Setenv("DATABASE_URL", validURL)
		t.Setenv("PORTAL_JWT_SECRET", validSecret)
		t.Setenv("SERVICE_API_KEY", validSecret)
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

	t.Run("requires the auth credentials", func(t *testing.T) {
		// A service that starts without these serves every schedule to anyone who
		// asks. Refusing to boot is the only safe default, so each one is checked
		// missing and too-short.
		for _, key := range []string{"PORTAL_JWT_SECRET", "SERVICE_API_KEY"} {
			t.Run(key+" missing", func(t *testing.T) {
				setRequired(t)
				t.Setenv(key, "")
				if _, err := Load(); err == nil {
					t.Errorf("expected an error when %s is unset", key)
				}
			})
			t.Run(key+" too short", func(t *testing.T) {
				setRequired(t)
				t.Setenv(key, "short")
				if _, err := Load(); err == nil {
					t.Errorf("expected an error when %s is below the minimum length", key)
				}
			})
			t.Run(key+" value never appears in the error", func(t *testing.T) {
				setRequired(t)
				t.Setenv(key, "tooshort-but-secret")
				_, err := Load()
				if err == nil {
					t.Fatalf("expected an error for a short %s", key)
				}
				if strings.Contains(err.Error(), "tooshort-but-secret") {
					t.Errorf("%s value leaked into the error: %v", key, err)
				}
			})
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

	// cmd/cadenceos, cmd/materialize, cmd/sweep and cmd/migrate all call Load and none
	// of them sends email -- requiring Postmark config across the board would force
	// every deployment to configure it just to run a migration. Only cmd/notify needs
	// it, via RequireNotifications below.
	t.Run("does not require the notification fields", func(t *testing.T) {
		setRequired(t)
		if _, err := Load(); err != nil {
			t.Fatalf("Load without any POSTMARK_*/NOTIFICATION_* set: %v", err)
		}
	})
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
