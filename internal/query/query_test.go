// Tests for the shared query layer: funnel/retention wiring against a real
// store, and the rollup fast path for daily counts (including partial-day
// ranges that must NOT be answered from rollups).
package query

import (
	"testing"
	"time"

	"github.com/JaydenCJ/eventfold/internal/event"
	"github.com/JaydenCJ/eventfold/internal/funnel"
	"github.com/JaydenCJ/eventfold/internal/retention"
	"github.com/JaydenCJ/eventfold/internal/rollup"
	"github.com/JaydenCJ/eventfold/internal/store"
	"github.com/JaydenCJ/eventfold/internal/timeq"
)

var base = time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)

func ev(name, user string, day, minute int) event.Event {
	return event.Event{
		Name: name,
		User: user,
		Time: base.AddDate(0, 0, day).Add(time.Duration(minute) * time.Minute),
	}
}

func seed(t *testing.T, events ...event.Event) *store.Store {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(events); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestFunnelAcrossDayPartitions(t *testing.T) {
	// Steps land on different days: the per-user merge must span partitions.
	s := seed(t,
		ev("signup", "u1", 0, 0),
		ev("activate", "u1", 1, 0),
		ev("pay", "u1", 2, 0),
		ev("signup", "u2", 0, 0),
	)
	cfg := funnel.Config{Steps: []string{"signup", "activate", "pay"}, Window: 7 * 24 * time.Hour}
	res, stats, err := Funnel(s, cfg, timeq.Range{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Entered != 2 || res.Steps[2].Users != 1 {
		t.Fatalf("res = %+v", res)
	}
	if stats.Matched != 4 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestFunnelRangeAndValidation(t *testing.T) {
	{
		s := seed(t,
			ev("signup", "u1", 0, 0), ev("pay", "u1", 0, 30),
			ev("signup", "u2", 10, 0), ev("pay", "u2", 10, 30),
		)
		r, _ := timeq.ParseRange("2026-06-01", "2026-06-02")
		cfg := funnel.Config{Steps: []string{"signup", "pay"}, Window: time.Hour}
		res, _, err := Funnel(s, cfg, r)
		if err != nil {
			t.Fatal(err)
		}
		if res.Entered != 1 {
			t.Fatalf("range should keep only u1: %+v", res)
		}
	}
	{
		s := seed(t)
		if _, _, err := Funnel(s, funnel.Config{Steps: []string{"only"}, Window: time.Hour}, timeq.Range{}); err == nil {
			t.Fatal("one-step funnel should error")
		}
	}
}

func TestRetentionActivityWiring(t *testing.T) {
	{
		s := seed(t,
			ev("signup", "u1", 0, 0),
			ev("custom_thing", "u1", 1, 0),
		)
		cfg := retention.Config{Cohort: "signup", Activity: "", Period: "day", Periods: 2}
		res, _, err := Retention(s, cfg, timeq.Range{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Rows[0].Retained[1] != 1 {
			t.Fatalf("any-event activity lost in scan wiring: %+v", res.Rows[0])
		}
	}
	{
		s := seed(t,
			ev("signup", "u1", 0, 0),
			ev("open", "u1", 1, 0),
			ev("noise", "u1", 1, 0),
		)
		cfg := retention.Config{Cohort: "signup", Activity: "open", Period: "day", Periods: 2}
		res, stats, err := Retention(s, cfg, timeq.Range{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Rows[0].Retained[1] != 1 {
			t.Fatalf("rows = %+v", res.Rows)
		}
		// The scan filter must have excluded the noise event.
		if stats.Matched != 2 {
			t.Fatalf("matched = %d, want 2 (noise filtered at scan level)", stats.Matched)
		}
	}
}

func TestCountDailyPrefersFreshRollups(t *testing.T) {
	s := seed(t,
		ev("pageview", "u1", 0, 0), ev("pageview", "u2", 0, 5),
		ev("pageview", "u1", 1, 0),
	)
	if _, err := rollup.Build(s, false); err != nil {
		t.Fatal(err)
	}
	buckets, err := Count(s, "pageview", "day", timeq.Range{})
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 2 {
		t.Fatalf("buckets = %+v", buckets)
	}
	for _, b := range buckets {
		if !b.FromRollup {
			t.Errorf("bucket %s should come from a rollup", b.Start)
		}
	}
	if buckets[0].Count != 2 || buckets[0].Users != 2 || buckets[1].Count != 1 {
		t.Fatalf("buckets = %+v", buckets)
	}
}

func TestCountDailyScanFallbacks(t *testing.T) {
	{
		s := seed(t, ev("pageview", "u1", 0, 0))
		if _, err := rollup.Build(s, false); err != nil {
			t.Fatal(err)
		}
		// Append after the rollup: the fingerprint breaks, so the day must be
		// scanned and reflect the new event immediately.
		if _, err := s.Append([]event.Event{ev("pageview", "u2", 0, 10)}); err != nil {
			t.Fatal(err)
		}
		buckets, err := Count(s, "pageview", "day", timeq.Range{})
		if err != nil {
			t.Fatal(err)
		}
		if buckets[0].FromRollup {
			t.Fatalf("stale rollup must not be used")
		}
		if buckets[0].Count != 2 || buckets[0].Users != 2 {
			t.Fatalf("bucket = %+v", buckets[0])
		}
	}
	{
		s := seed(t, ev("pageview", "u1", 0, 0))
		buckets, err := Count(s, "pageview", "day", timeq.Range{})
		if err != nil {
			t.Fatal(err)
		}
		if len(buckets) != 1 || buckets[0].FromRollup || buckets[0].Count != 1 {
			t.Fatalf("buckets = %+v", buckets)
		}
	}
}

func TestCountWeeklyAggregatesUniquesExactly(t *testing.T) {
	// u1 is active on two days of the same week: weekly uniques must be 1,
	// which summing daily rollups would get wrong (2).
	s := seed(t,
		ev("pageview", "u1", 0, 0),
		ev("pageview", "u1", 2, 0),
		ev("pageview", "u2", 2, 0),
		ev("pageview", "u1", 7, 0), // next week
	)
	if _, err := rollup.Build(s, false); err != nil {
		t.Fatal(err)
	}
	buckets, err := Count(s, "pageview", "week", timeq.Range{})
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 2 {
		t.Fatalf("buckets = %+v", buckets)
	}
	if buckets[0].Start != "2026-06-01" || buckets[0].Count != 3 || buckets[0].Users != 2 {
		t.Fatalf("week 0 = %+v", buckets[0])
	}
	if buckets[1].Start != "2026-06-08" || buckets[1].Users != 1 {
		t.Fatalf("week 1 = %+v", buckets[1])
	}
}

func TestCountRangeRollupEligibility(t *testing.T) {
	{
		s := seed(t,
			ev("pageview", "u1", 0, 0),
			ev("pageview", "u2", 1, 0),
		)
		if _, err := rollup.Build(s, false); err != nil {
			t.Fatal(err)
		}
		r, _ := timeq.ParseRange("2026-06-01", "2026-06-01")
		buckets, err := Count(s, "pageview", "day", r)
		if err != nil {
			t.Fatal(err)
		}
		if len(buckets) != 1 || buckets[0].Start != "2026-06-01" {
			t.Fatalf("buckets = %+v", buckets)
		}
		// The whole 06-01 day is inside the range, so the rollup is usable.
		if !buckets[0].FromRollup {
			t.Fatalf("whole-day range should still use the rollup")
		}
	}
	{
		// A range bound that cuts into a day makes the whole-day rollup answer
		// wrong; that day must be scanned and filtered instead.
		s := seed(t,
			ev("pageview", "u1", 0, 0),   // 09:00
			ev("pageview", "u2", 0, 300), // 14:00
		)
		if _, err := rollup.Build(s, false); err != nil {
			t.Fatal(err)
		}
		since, _ := timeq.ParseDate("2026-06-01")
		r := timeq.Range{Since: since, Until: since.Add(12 * time.Hour)} // until noon
		buckets, err := Count(s, "pageview", "day", r)
		if err != nil {
			t.Fatal(err)
		}
		if len(buckets) != 1 || buckets[0].FromRollup {
			t.Fatalf("partial day must scan: %+v", buckets)
		}
		if buckets[0].Count != 1 || buckets[0].Users != 1 {
			t.Fatalf("partial-day filter wrong: %+v", buckets[0])
		}
	}
}

func TestCountZeroCountDayStillListed(t *testing.T) {
	// A day partition that exists but contains no matching events shows a
	// zero bucket rather than disappearing, so trends have no silent holes.
	s := seed(t,
		ev("pageview", "u1", 0, 0),
		ev("signup", "u2", 1, 0),
	)
	buckets, err := Count(s, "pageview", "day", timeq.Range{})
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 2 || buckets[1].Count != 0 {
		t.Fatalf("buckets = %+v", buckets)
	}
}

func TestEventsListing(t *testing.T) {
	{
		s := seed(t,
			ev("beta", "u1", 0, 0), ev("beta", "u2", 0, 1),
			ev("alpha", "u1", 0, 2), ev("alpha", "u1", 0, 3),
			ev("gamma", "u1", 0, 4),
		)
		aggs, _, err := Events(s, timeq.Range{})
		if err != nil {
			t.Fatal(err)
		}
		if len(aggs) != 3 {
			t.Fatalf("aggs = %+v", aggs)
		}
		// alpha and beta both have count 2; alpha sorts first by name.
		if aggs[0].Name != "alpha" || aggs[1].Name != "beta" || aggs[2].Name != "gamma" {
			t.Fatalf("order = %+v", aggs)
		}
		if aggs[0].Users != 1 || aggs[1].Users != 2 {
			t.Fatalf("uniques = %+v", aggs)
		}
	}
	{
		s := seed(t)
		aggs, _, err := Events(s, timeq.Range{})
		if err != nil {
			t.Fatal(err)
		}
		if len(aggs) != 0 {
			t.Fatalf("aggs = %+v", aggs)
		}
	}
}
