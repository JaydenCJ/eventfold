package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/JaydenCJ/eventfold/internal/funnel"
	"github.com/JaydenCJ/eventfold/internal/query"
	"github.com/JaydenCJ/eventfold/internal/render"
	"github.com/JaydenCJ/eventfold/internal/retention"
	"github.com/JaydenCJ/eventfold/internal/rollup"
	"github.com/JaydenCJ/eventfold/internal/timeq"
)

func runFunnel(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("funnel", stderr)
	dir := fs.String("dir", DefaultDir, "data directory")
	steps := fs.String("steps", "", "comma-separated ordered step events (required, ≥2)")
	window := fs.String("window", "7d", "max time from first step (e.g. 30m, 6h, 7d, 2w)")
	by := fs.String("by", "", "segment by this property of the first-step event")
	since := fs.String("since", "", "start date YYYY-MM-DD (inclusive)")
	until := fs.String("until", "", "end date YYYY-MM-DD (inclusive)")
	format := fs.String("format", "text", "output format: text or json")
	fs.Usage = func() {
		fmt.Fprintln(stderr, `usage: eventfold funnel --steps "a,b,c" [--window 7d] [--by prop] [flags]`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if err := checkFormat(*format); err != nil {
		return usageErr(stderr, "%v", err)
	}
	if *steps == "" {
		return usageErr(stderr, `--steps is required, e.g. --steps "signup,activate,subscribe"`)
	}
	win, err := timeq.ParseWindow(*window)
	if err != nil {
		return usageErr(stderr, "%v", err)
	}
	r, err := parseRangeFlags(*since, *until)
	if err != nil {
		return usageErr(stderr, "%v", err)
	}
	cfg := funnel.Config{Steps: splitSteps(*steps), Window: win, By: *by}
	if err := cfg.Validate(); err != nil {
		return usageErr(stderr, "%v", err)
	}
	st, err := openStore(*dir)
	if err != nil {
		return runtimeErr(stderr, err)
	}
	res, _, err := query.Funnel(st, cfg, r)
	if err != nil {
		return runtimeErr(stderr, err)
	}
	if *format == "json" {
		if err := render.JSON(stdout, "funnel", res); err != nil {
			return runtimeErr(stderr, err)
		}
		return ExitOK
	}
	render.Funnel(stdout, cfg, res, render.RangeLabel(*since, *until))
	return ExitOK
}

func runRetention(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("retention", stderr)
	dir := fs.String("dir", DefaultDir, "data directory")
	cohort := fs.String("cohort", "", "cohort-defining event (required), e.g. signup")
	activity := fs.String("activity", "", "activity event; empty means any event counts")
	period := fs.String("period", "week", "cohort period: day or week")
	periods := fs.Int("periods", 8, "number of periods to show (2-52)")
	since := fs.String("since", "", "start date YYYY-MM-DD (inclusive)")
	until := fs.String("until", "", "end date YYYY-MM-DD (inclusive)")
	format := fs.String("format", "text", "output format: text or json")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: eventfold retention --cohort signup [--activity X] [--period week] [flags]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if err := checkFormat(*format); err != nil {
		return usageErr(stderr, "%v", err)
	}
	if *cohort == "" {
		return usageErr(stderr, "--cohort is required, e.g. --cohort signup")
	}
	r, err := parseRangeFlags(*since, *until)
	if err != nil {
		return usageErr(stderr, "%v", err)
	}
	cfg := retention.Config{Cohort: *cohort, Activity: *activity, Period: *period, Periods: *periods}
	if err := cfg.Validate(); err != nil {
		return usageErr(stderr, "%v", err)
	}
	st, err := openStore(*dir)
	if err != nil {
		return runtimeErr(stderr, err)
	}
	res, _, err := query.Retention(st, cfg, r)
	if err != nil {
		return runtimeErr(stderr, err)
	}
	if *format == "json" {
		if err := render.JSON(stdout, "retention", res); err != nil {
			return runtimeErr(stderr, err)
		}
		return ExitOK
	}
	render.Retention(stdout, cfg, res, render.RangeLabel(*since, *until))
	return ExitOK
}

func runCount(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("count", stderr)
	dir := fs.String("dir", DefaultDir, "data directory")
	eventName := fs.String("event", "", "event name to count (required)")
	by := fs.String("by", "day", "bucket size: day or week")
	since := fs.String("since", "", "start date YYYY-MM-DD (inclusive)")
	until := fs.String("until", "", "end date YYYY-MM-DD (inclusive)")
	format := fs.String("format", "text", "output format: text or json")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: eventfold count --event pageview [--by day|week] [flags]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if err := checkFormat(*format); err != nil {
		return usageErr(stderr, "%v", err)
	}
	if *eventName == "" {
		return usageErr(stderr, "--event is required, e.g. --event pageview")
	}
	if !timeq.ValidPeriod(*by) {
		return usageErr(stderr, "--by must be day or week, got %q", *by)
	}
	r, err := parseRangeFlags(*since, *until)
	if err != nil {
		return usageErr(stderr, "%v", err)
	}
	st, err := openStore(*dir)
	if err != nil {
		return runtimeErr(stderr, err)
	}
	buckets, err := query.Count(st, *eventName, *by, r)
	if err != nil {
		return runtimeErr(stderr, err)
	}
	if *format == "json" {
		if err := render.JSON(stdout, "count", buckets); err != nil {
			return runtimeErr(stderr, err)
		}
		return ExitOK
	}
	render.Counts(stdout, *eventName, *by, buckets)
	return ExitOK
}

func runEvents(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("events", stderr)
	dir := fs.String("dir", DefaultDir, "data directory")
	since := fs.String("since", "", "start date YYYY-MM-DD (inclusive)")
	until := fs.String("until", "", "end date YYYY-MM-DD (inclusive)")
	format := fs.String("format", "text", "output format: text or json")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: eventfold events [--since D] [--until D] [--format text|json]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if err := checkFormat(*format); err != nil {
		return usageErr(stderr, "%v", err)
	}
	r, err := parseRangeFlags(*since, *until)
	if err != nil {
		return usageErr(stderr, "%v", err)
	}
	st, err := openStore(*dir)
	if err != nil {
		return runtimeErr(stderr, err)
	}
	aggs, _, err := query.Events(st, r)
	if err != nil {
		return runtimeErr(stderr, err)
	}
	if *format == "json" {
		if err := render.JSON(stdout, "events", aggs); err != nil {
			return runtimeErr(stderr, err)
		}
		return ExitOK
	}
	render.Events(stdout, aggs, render.RangeLabel(*since, *until))
	return ExitOK
}

func runRollup(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("rollup", stderr)
	dir := fs.String("dir", DefaultDir, "data directory")
	force := fs.Bool("force", false, "rebuild even when the rollup is fresh")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: eventfold rollup [--dir PATH] [--force]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	st, err := openStore(*dir)
	if err != nil {
		return runtimeErr(stderr, err)
	}
	stats, err := rollup.Build(st, *force)
	if err != nil {
		return runtimeErr(stderr, err)
	}
	for _, d := range stats.Built {
		fmt.Fprintf(stdout, "  %s  built\n", d)
	}
	for _, d := range stats.Fresh {
		fmt.Fprintf(stdout, "  %s  fresh\n", d)
	}
	fmt.Fprintf(stdout, "rollups: %d built, %d already fresh\n", len(stats.Built), len(stats.Fresh))
	return ExitOK
}

// splitSteps parses "a, b ,c" into trimmed step names, keeping empties so
// funnel.Config.Validate can point at them precisely.
func splitSteps(s string) []string {
	parts := strings.Split(s, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}
