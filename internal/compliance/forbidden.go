// Package compliance enforces the spec §2 compliance boundary at build time.
//
// The service stores interval_days and nothing that implies consumption. Spec §2 is
// blunt about why: "The instant a field named doses_per_day appears in a migration,
// this stops being a commerce tool and becomes a treatment app: FTC/FDA exposure,
// processor risk, and a materially different insurance conversation."
//
// A rule with those consequences cannot depend on every reviewer remembering it, so
// spec §10 asks for "a schema test that fails the build if a forbidden column name
// appears — cheap insurance on §2." This package is that mechanism. Its test runs as
// part of `make test`, so it gates every pull request through the normal test check.
//
// # What it checks, and what it cannot
//
// It scans *identifiers*: Go declarations, field names and struct tags, and SQL
// table and column names. It deliberately does not scan comments or prose, because
// the text that describes this prohibition necessarily contains the words it bans —
// a guard that flagged its own documentation would be turned off within a week.
//
// That leaves customer-facing copy to human review. Every surface must say "when to
// reorder," never "when to take"; that criterion lives in
// .claude/agents/security-reviewer.md and in the pull request template.
package compliance

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// ForbiddenPattern is one banned concept and the reason it is banned.
//
// Patterns match against identifiers normalized to lower_snake_case, so DosesPerDay,
// dosesPerDay and doses_per_day are all the same string by the time they are tested.
type ForbiddenPattern struct {
	Name   string
	Reason string
	re     *regexp.Regexp
}

// word builds a pattern anchored to underscore-separated word boundaries. Regexp's
// own \b treats _ as a word character, so \badherence\b would not match
// adherence_streak — exactly the miss this guard cannot afford.
func word(expr string) *regexp.Regexp {
	return regexp.MustCompile(`(^|_)(` + expr + `)(_|$)`)
}

// forbidden lists the identifier shapes that turn a commerce tool into a treatment
// app. Each is deliberately broad: a false positive costs a rename and a
// conversation, while a miss costs a regulatory problem. Spec §2 names the concepts;
// these are the spellings they arrive as.
var forbidden = []ForbiddenPattern{
	{
		Name:   "usage rate",
		Reason: "spec §2 forbids units-per-day, servings-per-day, or any usage rate",
		re:     word(`(dose|doses|serving|servings|unit|units|pill|pills|capsule|capsules|tablet|tablets|injection|injections)_per_(day|week|month)|usage_rate|consumption_rate|daily_(dose|doses|amount|intake)`),
	},
	{
		Name:   "supply depletion",
		Reason: "spec §2 forbids doses remaining, days-left, or supply-depletion projections",
		re:     word(`(dose|doses|supply|serving|servings)_(remaining|left)|(day|days|week|weeks)_(remaining|left)|depletion|reorder_point|running_out|runs_out|will_run_out`),
	},
	{
		Name:   "adherence tracking",
		Reason: "spec §2 forbids adherence streaks, missed-dose reminders, and intake logging",
		re:     word(`adherence|adherence_streak|compliance_(streak|rate|score)|missed_dose|missed_doses|intake|intake_log|dose_(log|taken|time|times|reminder|schedule)|taken_at|last_taken`),
	},
	{
		Name:   "outcome tracking",
		Reason: "spec §2 forbids outcome tracking, symptom logs, and goal progress",
		re:     word(`symptom|symptoms|outcome_(track|tracking|log|record)|goal_(progress|tracking)|side_effect|side_effects`),
	},
	{
		Name: "cadence recommendation",
		Reason: "spec §2 forbids any per-compound cadence recommendation, authored or model-generated. " +
			"A documented per-SKU default is a merchandising decision and is fine; a recommendation is not",
		re: word(`(recommended|suggested|optimal|ideal|advised)_(cadence|interval|dose|dosage|frequency|schedule|days)|dosage|dosing|protocol_(interval|cadence)`),
	},
}

// Violation is one forbidden identifier found in one place.
type Violation struct {
	Path       string
	Line       int
	Identifier string
	Pattern    string
	Reason     string
}

func (v Violation) String() string {
	return fmt.Sprintf("%s:%d: identifier %q is a %s violation\n    %s",
		v.Path, v.Line, v.Identifier, v.Pattern, v.Reason)
}

// normalize converts an identifier to lower_snake_case so that camelCase, PascalCase
// and snake_case spellings of the same concept compare equal.
func normalize(ident string) string {
	var b strings.Builder
	runes := []rune(ident)
	for i, r := range runes {
		switch {
		case r == '-' || r == ' ' || r == '.':
			b.WriteRune('_')
		case unicode.IsUpper(r):
			// Insert a separator at a lower->upper transition (doseCount) and at the
			// end of an acronym run (SKUName), but not inside one.
			if i > 0 && (unicode.IsLower(runes[i-1]) || unicode.IsDigit(runes[i-1]) ||
				(i+1 < len(runes) && unicode.IsLower(runes[i+1]) && unicode.IsUpper(runes[i-1]))) {
				b.WriteRune('_')
			}
			b.WriteRune(unicode.ToLower(r))
		default:
			b.WriteRune(r)
		}
	}
	// Collapse repeated separators so a__b and a_b behave the same.
	return regexp.MustCompile(`_+`).ReplaceAllString(b.String(), "_")
}

// check tests one identifier and appends any violation it triggers.
func check(found []Violation, path string, line int, ident string) []Violation {
	n := normalize(ident)
	for _, p := range forbidden {
		if p.re.MatchString(n) {
			found = append(found, Violation{
				Path:       path,
				Line:       line,
				Identifier: ident,
				Pattern:    p.Name,
				Reason:     p.Reason,
			})
		}
	}
	return found
}

var (
	sqlLineComment  = regexp.MustCompile(`--[^\n]*`)
	sqlBlockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	sqlIdent        = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
	goStructTagKey  = regexp.MustCompile(`(?:json|db|sqlc):"([^"]*)"`)
)

// Scan walks fsys and reports every forbidden identifier it finds in Go source and
// SQL migrations.
func Scan(fsys fs.FS, root string) ([]Violation, error) {
	var found []Violation

	err := fs.WalkDir(fsys, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", ".postgres-data":
				return fs.SkipDir
			}
			return nil
		}

		switch strings.ToLower(filepath.Ext(path)) {
		case ".go":
			v, err := scanGo(fsys, path)
			if err != nil {
				return err
			}
			found = append(found, v...)
		case ".sql":
			v, err := scanSQL(fsys, path)
			if err != nil {
				return err
			}
			found = append(found, v...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

// scanGo reports forbidden identifiers in one Go file: every declared and referenced
// name, plus the column and field names inside struct tags. Comments and string
// literals are not scanned — see the package comment.
func scanGo(fsys fs.FS, path string) ([]Violation, error) {
	src, err := fs.ReadFile(fsys, path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		// An unparseable file is a compile error the build will report on its own.
		// Skipping it here keeps this guard from masking the real message.
		return nil, nil //nolint:nilerr
	}

	var found []Violation
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.Ident:
			found = check(found, path, fset.Position(node.Pos()).Line, node.Name)
		case *ast.Field:
			if node.Tag == nil {
				return true
			}
			line := fset.Position(node.Tag.Pos()).Line
			for _, m := range goStructTagKey.FindAllStringSubmatch(node.Tag.Value, -1) {
				name, _, _ := strings.Cut(m[1], ",")
				if name != "" && name != "-" {
					found = check(found, path, line, name)
				}
			}
		}
		return true
	})
	return found, nil
}

// scanSQL reports forbidden identifiers in one SQL file. Comments are stripped
// first: a migration commenting on what it deliberately does not store is good
// practice, not a violation.
func scanSQL(fsys fs.FS, path string) ([]Violation, error) {
	src, err := fs.ReadFile(fsys, path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var found []Violation
	for i, line := range strings.Split(string(src), "\n") {
		stripped := sqlLineComment.ReplaceAllString(sqlBlockComment.ReplaceAllString(line, " "), "")
		for _, tok := range sqlIdent.FindAllString(stripped, -1) {
			found = check(found, path, i+1, tok)
		}
	}
	return found, nil
}
