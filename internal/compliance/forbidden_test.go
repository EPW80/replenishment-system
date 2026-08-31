package compliance

import (
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

// TestRepositoryHasNoForbiddenIdentifiers is the gate spec §10 asks for: "a schema
// test that fails the build if a forbidden column name appears — cheap insurance on
// §2."
//
// If this fails, the fix is to rename the field or reconsider the feature. It is
// never to weaken the guard. See CLAUDE.md, "Compliance boundary".
func TestRepositoryHasNoForbiddenIdentifiers(t *testing.T) {
	repoRoot := os.DirFS("../..")

	violations, err := Scan(repoRoot, ".")
	if err != nil {
		t.Fatalf("scan repository: %v", err)
	}

	for _, v := range violations {
		t.Errorf("compliance boundary violation\n%s", v)
	}
	if len(violations) > 0 {
		t.Logf("\n%d violation(s). The service stores interval_days and nothing implying "+
			"consumption (spec §2). Rename the field or drop the feature — do not weaken "+
			"this guard to make a change pass.", len(violations))
	}
}

// TestGuardActuallyFires is the point of the whole package. A guard that has never
// failed is not known to work: if Scan silently matched nothing — a broken regexp, a
// walk that skipped everything, an extension filter that excluded .sql — the test
// above would pass on a repository that had gone badly wrong.
//
// Each case plants a forbidden identifier of the shape spec §2 names and asserts it
// is caught.
func TestGuardActuallyFires(t *testing.T) {
	cases := []struct {
		name    string
		file    string
		content string
		want    string
	}{
		{
			name:    "doses_per_day column in a migration",
			file:    "migrations/00002_bad.sql",
			content: "ALTER TABLE schedules ADD COLUMN doses_per_day integer;",
			want:    "usage rate",
		},
		{
			name:    "servings per day in a Go struct",
			file:    "internal/domain/bad.go",
			content: "package p\ntype S struct { ServingsPerDay int }",
			want:    "usage rate",
		},
		{
			name:    "doses remaining",
			file:    "internal/store/bad.go",
			content: "package p\nfunc dosesRemaining() int { return 0 }",
			want:    "supply depletion",
		},
		{
			name:    "days-left projection in a DTO",
			file:    "internal/httpapi/bad.go",
			content: "package p\ntype R struct { DaysRemaining int }",
			want:    "supply depletion",
		},
		{
			name:    "adherence streak",
			file:    "internal/domain/bad.go",
			content: "package p\nvar adherenceStreak int",
			want:    "adherence tracking",
		},
		{
			name:    "missed dose reminder",
			file:    "internal/domain/bad.go",
			content: "package p\nconst missedDoseReminder = true",
			want:    "adherence tracking",
		},
		{
			name:    "intake logging",
			file:    "migrations/00003_bad.sql",
			content: "CREATE TABLE intake_log (id uuid);",
			want:    "adherence tracking",
		},
		{
			name:    "symptom log",
			file:    "migrations/00004_bad.sql",
			content: "CREATE TABLE symptom_entries (id uuid);",
			want:    "outcome tracking",
		},
		{
			name:    "recommended cadence",
			file:    "internal/domain/bad.go",
			content: "package p\nfunc recommendedCadence() int { return 30 }",
			want:    "cadence recommendation",
		},
		{
			name:    "dosage as a field name",
			file:    "internal/httpapi/bad.go",
			content: "package p\ntype R struct { Dosage int }",
			want:    "cadence recommendation",
		},
		{
			name:    "forbidden name only in a json tag",
			file:    "internal/httpapi/bad.go",
			content: "package p\ntype R struct { N int `json:\"doses_remaining\"` }",
			want:    "supply depletion",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fsys := fstest.MapFS{tc.file: &fstest.MapFile{Data: []byte(tc.content)}}

			violations, err := Scan(fsys, ".")
			if err != nil {
				t.Fatalf("scan: %v", err)
			}
			if len(violations) == 0 {
				t.Fatalf("guard did not fire on %q — it would not have caught this in a real PR", tc.content)
			}
			var names []string
			for _, v := range violations {
				names = append(names, v.Pattern)
			}
			if !containsStr(names, tc.want) {
				t.Errorf("matched %v, want a %q violation", names, tc.want)
			}
		})
	}
}

// TestGuardAllowsLegitimateFields guards against the opposite failure: a pattern so
// broad that it blocks the schema the spec actually requires.
func TestGuardAllowsLegitimateFields(t *testing.T) {
	// Spec §2: "A default cadence per SKU is fine — it is a merchandising decision
	// derived from pack size and observed reorder behavior."
	allowed := []string{
		"ALTER TABLE schedules ADD COLUMN interval_days integer NOT NULL;",
		"CREATE TABLE occurrences (scheduled_for date NOT NULL);",
		"package p\ntype Schedule struct { IntervalDays int; AnchorDate int }",
		"package p\n// default_interval_days is a merchandising default derived from pack size\nvar defaultIntervalDays = 30",
		"package p\nfunc nextRunDate(anchor, n, intervalDays int) int { return anchor }",
		"package p\nconst defaultHorizon = 3 // planned occurrences ahead",
		"package p\n// when to reorder, never when to take\nvar reorderCopy = \"when to reorder\"",
		"package p\nvar reorderDiscountPct = 10",
	}

	for _, content := range allowed {
		t.Run(truncate(content), func(t *testing.T) {
			fsys := fstest.MapFS{"internal/domain/ok.go": &fstest.MapFile{Data: []byte(content)}}
			violations, err := Scan(fsys, ".")
			if err != nil {
				t.Fatalf("scan: %v", err)
			}
			for _, v := range violations {
				t.Errorf("false positive on legitimate field:\n%s", v)
			}
		})
	}
}

func containsStr(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

func truncate(s string) string {
	s = strings.ReplaceAll(s, " ", "_")
	if len(s) > 40 {
		return s[:40]
	}
	return s
}
