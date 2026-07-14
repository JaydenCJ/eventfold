// Tests for text/JSON rendering: byte-determinism, gauge geometry, duration
// formatting, and the JSON envelope shape.
package render

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/JaydenCJ/eventfold/internal/funnel"
	"github.com/JaydenCJ/eventfold/internal/query"
	"github.com/JaydenCJ/eventfold/internal/retention"
)

func TestGauge(t *testing.T) {
	{
		if g := Gauge(0, 10); g != strings.Repeat("░", 10) {
			t.Errorf("0%% gauge = %q", g)
		}
		if g := Gauge(100, 10); g != strings.Repeat("█", 10) {
			t.Errorf("100%% gauge = %q", g)
		}
		if g := Gauge(50, 10); g != strings.Repeat("█", 5)+strings.Repeat("░", 5) {
			t.Errorf("50%% gauge = %q", g)
		}
		// Out-of-range values clamp instead of panicking.
		if g := Gauge(150, 4); g != "████" {
			t.Errorf("clamped gauge = %q", g)
		}
		if g := Gauge(-5, 4); g != "░░░░" {
			t.Errorf("negative gauge = %q", g)
		}
	}
	{
		for pct := 0.0; pct <= 100; pct += 3.7 {
			g := Gauge(pct, 24)
			if n := len([]rune(g)); n != 24 {
				t.Fatalf("gauge width at %.1f%% = %d runes", pct, n)
			}
		}
	}
}

func TestFormatDuration(t *testing.T) {
	cases := map[float64]string{
		42:          "42s",
		89:          "89s",
		90:          "2m",
		600:         "10m",
		3 * 3600:    "3h",
		5400:        "1.5h",
		2 * 86400:   "2d",
		2.5 * 86400: "2.5d",
		40 * 3600:   "1.7d",
	}
	for secs, want := range cases {
		if got := FormatDuration(secs); got != want {
			t.Errorf("FormatDuration(%v) = %q, want %q", secs, got, want)
		}
	}
}

func TestJSONEnvelope(t *testing.T) {
	var buf bytes.Buffer
	if err := JSON(&buf, "funnel", map[string]int{"x": 1}); err != nil {
		t.Fatal(err)
	}
	var env struct {
		Tool          string         `json:"tool"`
		SchemaVersion int            `json:"schema_version"`
		Kind          string         `json:"kind"`
		Result        map[string]int `json:"result"`
	}
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	if env.Tool != "eventfold" || env.SchemaVersion != 1 || env.Kind != "funnel" || env.Result["x"] != 1 {
		t.Fatalf("envelope = %+v", env)
	}
}

func sampleFunnel() (funnel.Config, funnel.Result) {
	cfg := funnel.Config{Steps: []string{"signup", "pay"}, Window: 7 * 24 * time.Hour}
	res := funnel.Result{
		Entered: 4,
		Steps: []funnel.Step{
			{Name: "signup", Users: 4, PctOfFirst: 100, PctOfPrev: 100},
			{Name: "pay", Users: 1, PctOfFirst: 25, PctOfPrev: 25, MedianSecs: 600, MedianCount: 1},
		},
	}
	return cfg, res
}

func TestFunnelText(t *testing.T) {
	{
		var buf bytes.Buffer
		cfg, res := sampleFunnel()
		Funnel(&buf, cfg, res, "all time")
		out := buf.String()
		for _, want := range []string{"signup → pay", "window: 7d", "entered: 4 users", "25.0%", "10m", "█"} {
			if !strings.Contains(out, want) {
				t.Errorf("funnel text missing %q:\n%s", want, out)
			}
		}
	}
	{
		cfg, res := sampleFunnel()
		var a, b bytes.Buffer
		Funnel(&a, cfg, res, "all time")
		Funnel(&b, cfg, res, "all time")
		if a.String() != b.String() {
			t.Fatalf("funnel render not byte-identical")
		}
	}
	{
		cfg, res := sampleFunnel()
		cfg.By = "plan"
		res.Segments = []funnel.Segment{{Key: "pro", Entered: 2, Steps: res.Steps}}
		var buf bytes.Buffer
		Funnel(&buf, cfg, res, "all time")
		if !strings.Contains(buf.String(), "plan = pro  (2 entered)") {
			t.Fatalf("segment header missing:\n%s", buf.String())
		}
	}
}

func TestRetentionText(t *testing.T) {
	{
		cfg := retention.Config{Cohort: "signup", Activity: "open", Period: "week", Periods: 3}
		res := retention.Result{Rows: []retention.Row{
			{Cohort: "2026-06-01", Size: 10, Retained: []int{10, 4, 2}, Percent: []float64{100, 40, 20}},
		}}
		var buf bytes.Buffer
		Retention(&buf, cfg, res, "all time")
		out := buf.String()
		for _, want := range []string{"cohort=signup", "p0", "p2", "100.0%", "40.0%", "20.0%"} {
			if !strings.Contains(out, want) {
				t.Errorf("retention text missing %q:\n%s", want, out)
			}
		}
	}
	{
		cfg := retention.Config{Cohort: "signup", Period: "week", Periods: 2}
		var buf bytes.Buffer
		Retention(&buf, cfg, retention.Result{}, "all time")
		if !strings.Contains(buf.String(), "no cohorts in range") {
			t.Fatalf("empty retention message missing:\n%s", buf.String())
		}
		// The unset activity renders as "any event", never as an empty string.
		if !strings.Contains(buf.String(), "activity=any event") {
			t.Fatalf("activity label missing:\n%s", buf.String())
		}
	}
}

func TestCountsText(t *testing.T) {
	{
		var buf bytes.Buffer
		Counts(&buf, "pageview", "day", []query.Bucket{
			{Start: "2026-06-01", Count: 12, Users: 5, FromRollup: true},
			{Start: "2026-06-02", Count: 7, Users: 3},
		})
		out := buf.String()
		for _, want := range []string{"count: pageview by day", "rollup", "scan", "19 events across 2 buckets"} {
			if !strings.Contains(out, want) {
				t.Errorf("counts text missing %q:\n%s", want, out)
			}
		}
	}
	{
		var buf bytes.Buffer
		Counts(&buf, "x", "day", nil)
		if !strings.Contains(buf.String(), "no events in range") {
			t.Fatalf("empty counts message missing:\n%s", buf.String())
		}
	}
}

func TestEventsTable(t *testing.T) {
	var buf bytes.Buffer
	Events(&buf, []query.EventAgg{
		{Name: "pageview", Count: 100, Users: 20},
		{Name: "signup", Count: 5, Users: 5},
	}, "2026-06-01 →")
	out := buf.String()
	for _, want := range []string{"range: 2026-06-01 →", "pageview", "100", "signup"} {
		if !strings.Contains(out, want) {
			t.Errorf("events text missing %q:\n%s", want, out)
		}
	}
}

func TestRangeLabel(t *testing.T) {
	cases := map[[2]string]string{
		{"", ""}:                     "all time",
		{"2026-06-01", ""}:           "2026-06-01 →",
		{"", "2026-06-30"}:           "→ 2026-06-30",
		{"2026-06-01", "2026-06-30"}: "2026-06-01 → 2026-06-30",
	}
	for in, want := range cases {
		if got := RangeLabel(in[0], in[1]); got != want {
			t.Errorf("RangeLabel(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}
