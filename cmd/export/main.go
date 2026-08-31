// Command export writes one read-model view as CSV.
//
// Spec §8 calls audience segments "exported to retargeting." This is that export
// mechanism: an operator or a scheduled job runs it and pipes the output into
// whatever consumes it, the same shape cmd/migrate and cmd/materialize already use
// for operational tasks.
//
// It deliberately has no HTTP counterpart. There is no authentication or
// authorization mechanism anywhere in this service yet — building one for a
// handful of admin routes here would mean designing it twice once Phase 6 needs a
// real one. See docs/adr/0006.
package main

import (
	"context"
	"database/sql"
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/EPW80/replenishment-system/internal/config"
	"github.com/EPW80/replenishment-system/internal/domain"
	"github.com/EPW80/replenishment-system/internal/readmodel"
)

func main() {
	view := flag.String("view", "", "view to export: cadence-distribution, churn-reasons, forecast, segments, cohorts")
	segment := flag.String("segment", "", "required for -view=segments: paused, failed, or canceled_within_90d")
	from := flag.String("from", "", "required for -view=forecast: start date, YYYY-MM-DD")
	to := flag.String("to", "", "required for -view=forecast: end date, YYYY-MM-DD")
	out := flag.String("out", "", "output file; defaults to stdout")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	if err := run(*view, *segment, *from, *to, *out); err != nil {
		log.Error("export failed", "error", err)
		os.Exit(1)
	}
}

func run(view, segment, from, to, outPath string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = db.Close() }()

	w := os.Stdout
	if outPath != "" {
		f, err := os.Create(outPath)
		if err != nil {
			return fmt.Errorf("create output file: %w", err)
		}
		defer func() { _ = f.Close() }()
		return writeView(context.Background(), readmodel.New(db), view, segment, from, to, f)
	}
	return writeView(context.Background(), readmodel.New(db), view, segment, from, to, w)
}

func writeView(ctx context.Context, rm readmodel.Repository, view, segment, from, to string, w io.Writer) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()

	switch view {
	case "cadence-distribution":
		return exportCadenceDistribution(ctx, rm, cw)
	case "churn-reasons":
		return exportChurnReasons(ctx, rm, cw)
	case "forecast":
		return exportForecast(ctx, rm, cw, from, to)
	case "segments":
		return exportSegments(ctx, rm, cw, segment)
	case "cohorts":
		return exportCohorts(ctx, rm, cw)
	case "":
		return fmt.Errorf("-view is required: cadence-distribution, churn-reasons, forecast, segments, cohorts")
	default:
		return fmt.Errorf("unknown -view %q", view)
	}
}

func exportCadenceDistribution(ctx context.Context, rm readmodel.Repository, cw *csv.Writer) error {
	rows, err := rm.CadenceDistribution(ctx)
	if err != nil {
		return err
	}
	if err := cw.Write([]string{"sku", "interval_days", "status", "schedule_count"}); err != nil {
		return err
	}
	for _, r := range rows {
		if err := cw.Write([]string{
			r.SKU, strconv.Itoa(r.IntervalDays), string(r.Status), strconv.Itoa(r.ScheduleCount),
		}); err != nil {
			return err
		}
	}
	return cw.Error()
}

func exportChurnReasons(ctx context.Context, rm readmodel.Repository, cw *csv.Writer) error {
	rows, err := rm.ChurnReasons(ctx)
	if err != nil {
		return err
	}
	if err := cw.Write([]string{"reason_code", "cancellation_count", "first_at", "last_at"}); err != nil {
		return err
	}
	for _, r := range rows {
		if err := cw.Write([]string{
			r.ReasonCode, strconv.Itoa(r.CancellationCount), formatTime(r.FirstAt), formatTime(r.LastAt),
		}); err != nil {
			return err
		}
	}
	return cw.Error()
}

func exportForecast(ctx context.Context, rm readmodel.Repository, cw *csv.Writer, from, to string) error {
	if from == "" || to == "" {
		return fmt.Errorf("-from and -to are both required for -view=forecast (YYYY-MM-DD)")
	}
	fromDate, err := parseDate(from)
	if err != nil {
		return fmt.Errorf("-from: %w", err)
	}
	toDate, err := parseDate(to)
	if err != nil {
		return fmt.Errorf("-to: %w", err)
	}

	rows, err := rm.OccurrenceForecast(ctx, fromDate, toDate)
	if err != nil {
		return err
	}
	if err := cw.Write([]string{"week_start", "sku", "occurrence_count", "unit_count"}); err != nil {
		return err
	}
	for _, r := range rows {
		if err := cw.Write([]string{
			r.WeekStart.String(), r.SKU, strconv.Itoa(r.OccurrenceCount), strconv.Itoa(r.UnitCount),
		}); err != nil {
			return err
		}
	}
	return cw.Error()
}

func exportSegments(ctx context.Context, rm readmodel.Repository, cw *csv.Writer, segment string) error {
	if segment == "" {
		return fmt.Errorf("-segment is required for -view=segments: %v", readmodel.ValidSegments)
	}
	rows, err := rm.AudienceSegment(ctx, readmodel.Segment(segment))
	if err != nil {
		return err
	}
	if err := cw.Write([]string{"customer_id", "schedule_id", "segment", "segment_since"}); err != nil {
		return err
	}
	for _, r := range rows {
		if err := cw.Write([]string{
			r.CustomerID, r.ScheduleID, r.Segment, formatTime(r.SegmentSince),
		}); err != nil {
			return err
		}
	}
	return cw.Error()
}

func exportCohorts(ctx context.Context, rm readmodel.Repository, cw *csv.Writer) error {
	rows, err := rm.CohortRetention(ctx)
	if err != nil {
		return err
	}
	if err := cw.Write([]string{"cohort_month", "interval_days", "status", "schedule_count"}); err != nil {
		return err
	}
	for _, r := range rows {
		if err := cw.Write([]string{
			r.CohortMonth.String(), strconv.Itoa(r.IntervalDays), string(r.Status), strconv.Itoa(r.ScheduleCount),
		}); err != nil {
			return err
		}
	}
	return cw.Error()
}

func parseDate(s string) (domain.Date, error) {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return domain.Date{}, fmt.Errorf("must be YYYY-MM-DD, got %q", s)
	}
	return domain.DateOf(t), nil
}

// formatTime renders a nullable timestamp as RFC 3339, or empty string when nil --
// never a sentinel like "N/A", which downstream CSV consumers would otherwise have
// to special-case.
func formatTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
