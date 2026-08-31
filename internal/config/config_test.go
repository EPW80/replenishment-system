package config

import "testing"

func TestLoad(t *testing.T) {
	const validURL = "postgres://u:p@localhost:5432/db?sslmode=disable"

	t.Run("requires DATABASE_URL", func(t *testing.T) {
		t.Setenv("DATABASE_URL", "")
		if _, err := Load(); err == nil {
			t.Fatal("expected an error when DATABASE_URL is unset; a service that silently starts against the wrong database is worse than one that refuses to start")
		}
	})

	t.Run("applies defaults", func(t *testing.T) {
		t.Setenv("DATABASE_URL", validURL)
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
				t.Setenv("DATABASE_URL", validURL)
				t.Setenv(tc.key, tc.value)
				if _, err := Load(); err == nil {
					t.Errorf("expected an error for %s=%q", tc.key, tc.value)
				}
			})
		}
	})

	t.Run("reads overrides", func(t *testing.T) {
		t.Setenv("DATABASE_URL", validURL)
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
}
