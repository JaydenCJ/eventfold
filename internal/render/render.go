// Package render turns query results into terminal text and stable JSON.
// All output is deterministic: identical inputs produce byte-identical
// output, which the test suite and smoke script rely on.
package render

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/JaydenCJ/eventfold/internal/funnel"
	"github.com/JaydenCJ/eventfold/internal/query"
	"github.com/JaydenCJ/eventfold/internal/retention"
	"github.com/JaydenCJ/eventfold/internal/timeq"
)

// GaugeWidth is the character width of funnel conversion gauges.
const GaugeWidth = 24

// JSON writes the standard machine envelope around any result payload.
func JSON(w io.Writer, kind string, payload any) error {
	env := struct {
		Tool          string `json:"tool"`
		SchemaVersion int    `json:"schema_version"`
		Kind          string `json:"kind"`
		Result        any    `json:"result"`
	}{Tool: "eventfold", SchemaVersion: 1, Kind: kind, Result: payload}
	raw, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(raw))
	return err
}

// Gauge renders a percentage bar like ████████░░░░░░░░.
func Gauge(pct float64, width int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := int(pct/100*float64(width) + 0.5)
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

// Funnel renders the funnel result as an aligned step table with gauges.
func Funnel(w io.Writer, cfg funnel.Config, res funnel.Result, rangeLabel string) {
	fmt.Fprintf(w, "funnel: %s\n", strings.Join(cfg.Steps, " → "))
	fmt.Fprintf(w, "window: %s   range: %s   entered: %d users\n\n",
		timeq.FormatWindow(cfg.Window), rangeLabel, res.Entered)
	writeFunnelSteps(w, res.Steps, "")
	for _, seg := range res.Segments {
		fmt.Fprintf(w, "\n%s = %s  (%d entered)\n", cfg.By, seg.Key, seg.Entered)
		writeFunnelSteps(w, seg.Steps, "")
	}
}

func writeFunnelSteps(w io.Writer, steps []funnel.Step, indent string) {
	nameW := len("step")
	for _, s := range steps {
		if len(s.Name) > nameW {
			nameW = len(s.Name)
		}
	}
	fmt.Fprintf(w, "%s%-*s  %-*s  %7s  %7s  %6s  %10s\n",
		indent, 2+nameW, "step", GaugeWidth, "", "users", "overall", "step%", "median")
	for i, s := range steps {
		median := "—"
		if i > 0 && s.MedianCount > 0 {
			median = FormatDuration(s.MedianSecs)
		}
		fmt.Fprintf(w, "%s%d. %-*s  %s  %7d  %6.1f%%  %5.1f%%  %10s\n",
			indent, i+1, nameW, s.Name, Gauge(s.PctOfFirst, GaugeWidth),
			s.Users, s.PctOfFirst, s.PctOfPrev, median)
	}
}

// FormatDuration renders seconds compactly: 42s, 7m, 3.5h, 2.1d.
func FormatDuration(secs float64) string {
	switch {
	case secs < 90:
		return fmt.Sprintf("%.0fs", secs)
	case secs < 90*60:
		return fmt.Sprintf("%.0fm", secs/60)
	case secs < 36*3600:
		return trimZero(fmt.Sprintf("%.1fh", secs/3600))
	default:
		return trimZero(fmt.Sprintf("%.1fd", secs/86400))
	}
}

func trimZero(s string) string {
	return strings.Replace(s, ".0", "", 1)
}

// Retention renders the cohort triangle. Cells below the diagonal that the
// data cannot reach yet are left blank by the caller's data (zero rows are
// printed as 0.0%).
func Retention(w io.Writer, cfg retention.Config, res retention.Result, rangeLabel string) {
	activity := cfg.Activity
	if activity == "" {
		activity = "any event"
	}
	fmt.Fprintf(w, "retention: cohort=%s activity=%s period=%s   range: %s\n\n",
		cfg.Cohort, activity, cfg.Period, rangeLabel)
	if len(res.Rows) == 0 {
		fmt.Fprintln(w, "no cohorts in range")
		return
	}
	header := fmt.Sprintf("%-10s  %5s", "cohort", "size")
	for k := 0; k < cfg.Periods; k++ {
		header += fmt.Sprintf("  %6s", fmt.Sprintf("p%d", k))
	}
	fmt.Fprintln(w, header)
	for _, row := range res.Rows {
		line := fmt.Sprintf("%-10s  %5d", row.Cohort, row.Size)
		for _, pct := range row.Percent {
			line += fmt.Sprintf("  %5.1f%%", pct)
		}
		fmt.Fprintln(w, line)
	}
}

// Counts renders count buckets as an aligned table with a rollup marker.
func Counts(w io.Writer, name, by string, buckets []query.Bucket) {
	fmt.Fprintf(w, "count: %s by %s\n\n", name, by)
	if len(buckets) == 0 {
		fmt.Fprintln(w, "no events in range")
		return
	}
	fmt.Fprintf(w, "%-10s  %8s  %8s  %s\n", "bucket", "count", "users", "source")
	totalCount := 0
	for _, b := range buckets {
		src := "scan"
		if b.FromRollup {
			src = "rollup"
		}
		fmt.Fprintf(w, "%-10s  %8d  %8d  %s\n", b.Start, b.Count, b.Users, src)
		totalCount += b.Count
	}
	fmt.Fprintf(w, "\n%d event%s across %d bucket%s\n",
		totalCount, plural(totalCount), len(buckets), plural(len(buckets)))
}

// Events renders the distinct-event listing.
func Events(w io.Writer, aggs []query.EventAgg, rangeLabel string) {
	fmt.Fprintf(w, "events   range: %s\n\n", rangeLabel)
	if len(aggs) == 0 {
		fmt.Fprintln(w, "no events in range")
		return
	}
	nameW := len("event")
	for _, a := range aggs {
		if len(a.Name) > nameW {
			nameW = len(a.Name)
		}
	}
	fmt.Fprintf(w, "%-*s  %8s  %8s\n", nameW, "event", "count", "users")
	for _, a := range aggs {
		fmt.Fprintf(w, "%-*s  %8d  %8d\n", nameW, a.Name, a.Count, a.Users)
	}
}

// plural returns "s" when n != 1, so summaries never read "1 buckets".
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// RangeLabel names a range for headers: "all time", "2026-06-01 →", etc.
func RangeLabel(since, until string) string {
	switch {
	case since == "" && until == "":
		return "all time"
	case until == "":
		return since + " →"
	case since == "":
		return "→ " + until
	default:
		return since + " → " + until
	}
}
