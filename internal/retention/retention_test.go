// Tests for cohort retention: first-event cohort assignment, day and week
// periods, any-event vs named activity, and triangle shape.
package retention

import (
	"testing"
	"time"

	"github.com/JaydenCJ/eventfold/internal/event"
	"github.com/JaydenCJ/eventfold/internal/timeq"
)

// Monday, so day and week buckets line up predictably.
var base = time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)

func ev(name, user string, dayOffset int) event.Event {
	return event.Event{Name: name, User: user, Time: base.AddDate(0, 0, dayOffset)}
}

func mustNew(t *testing.T, cfg Config) *Analyzer {
	t.Helper()
	a, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

func TestConfigValidate(t *testing.T) {
	good := Config{Cohort: "signup", Period: timeq.PeriodWeek, Periods: 8}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	bad := []Config{
		{Cohort: "", Period: "week", Periods: 8},
		{Cohort: "s", Period: "month", Periods: 8},
		{Cohort: "s", Period: "day", Periods: 1},
		{Cohort: "s", Period: "day", Periods: MaxPeriods + 1},
	}
	for i, cfg := range bad {
		if err := cfg.Validate(); err == nil {
			t.Errorf("case %d should be rejected: %+v", i, cfg)
		}
	}
}

func TestDailyRetentionCounts(t *testing.T) {
	{
		a := mustNew(t, Config{Cohort: "signup", Activity: "open", Period: "day", Periods: 3})
		// u1: signs up day 0, opens day 0 and day 2.
		// u2: signs up day 0, opens day 1.
		// u3: signs up day 1, never opens.
		a.Feed(ev("signup", "u1", 0))
		a.Feed(ev("open", "u1", 0))
		a.Feed(ev("open", "u1", 2))
		a.Feed(ev("signup", "u2", 0))
		a.Feed(ev("open", "u2", 1))
		a.Feed(ev("signup", "u3", 1))
		res := a.Finalize()
		if len(res.Rows) != 2 {
			t.Fatalf("rows = %+v", res.Rows)
		}
		r0 := res.Rows[0]
		if r0.Cohort != "2026-06-01" || r0.Size != 2 {
			t.Fatalf("row 0 = %+v", r0)
		}
		// Day 0: only u1 opened. Day 1: only u2. Day 2: only u1.
		want := []int{1, 1, 1}
		for k, n := range want {
			if r0.Retained[k] != n {
				t.Errorf("cohort 0 period %d = %d, want %d", k, r0.Retained[k], n)
			}
		}
		if res.Rows[1].Size != 1 || res.Rows[1].Retained[0] != 0 {
			t.Fatalf("row 1 = %+v", res.Rows[1])
		}
	}
	{
		a := mustNew(t, Config{Cohort: "signup", Activity: "open", Period: "day", Periods: 2})
		a.Feed(ev("signup", "u1", 0))
		a.Feed(ev("signup", "u2", 0))
		a.Feed(ev("signup", "u3", 0))
		a.Feed(ev("signup", "u4", 0))
		a.Feed(ev("open", "u1", 1))
		res := a.Finalize()
		if got := res.Rows[0].Percent[1]; got != 25 {
			t.Fatalf("percent = %v, want 25", got)
		}
	}
}

func TestCohortIsFirstEventEvenWhenFedOutOfOrder(t *testing.T) {
	// The later signup arrives first; the user must still land in the
	// earlier cohort.
	a := mustNew(t, Config{Cohort: "signup", Period: "day", Periods: 2})
	a.Feed(ev("signup", "u1", 5))
	a.Feed(ev("signup", "u1", 0))
	res := a.Finalize()
	if len(res.Rows) != 1 || res.Rows[0].Cohort != "2026-06-01" {
		t.Fatalf("rows = %+v", res.Rows)
	}
}

func TestActivityMatching(t *testing.T) {
	{
		// With no activity event configured, any event keeps a user retained.
		a := mustNew(t, Config{Cohort: "signup", Activity: "", Period: "day", Periods: 2})
		a.Feed(ev("signup", "u1", 0))
		a.Feed(ev("totally_custom_event", "u1", 1))
		res := a.Finalize()
		if res.Rows[0].Retained[1] != 1 {
			t.Fatalf("any-event activity missed: %+v", res.Rows[0])
		}
		// Period 0 is trivially retained: the signup itself is activity.
		if res.Rows[0].Retained[0] != 1 {
			t.Fatalf("period 0 should count the cohort event itself: %+v", res.Rows[0])
		}
	}
	{
		a := mustNew(t, Config{Cohort: "signup", Activity: "open", Period: "day", Periods: 2})
		a.Feed(ev("signup", "u1", 0))
		a.Feed(ev("pageview", "u1", 1)) // not the configured activity
		res := a.Finalize()
		if res.Rows[0].Retained[1] != 0 {
			t.Fatalf("non-activity event counted: %+v", res.Rows[0])
		}
	}
}

func TestWeeklyRetentionBucketsOnMonday(t *testing.T) {
	a := mustNew(t, Config{Cohort: "signup", Activity: "open", Period: "week", Periods: 3})
	// Signup Wednesday of week 0; opens Sunday week 0 and Tuesday week 2.
	a.Feed(ev("signup", "u1", 2))
	a.Feed(ev("open", "u1", 6))
	a.Feed(ev("open", "u1", 15))
	res := a.Finalize()
	if len(res.Rows) != 1 || res.Rows[0].Cohort != "2026-06-01" {
		t.Fatalf("rows = %+v", res.Rows)
	}
	want := []int{1, 0, 1}
	for k, n := range want {
		if res.Rows[0].Retained[k] != n {
			t.Errorf("week %d retained = %d, want %d", k, res.Rows[0].Retained[k], n)
		}
	}
}

func TestNonMembersAndPastActivity(t *testing.T) {
	{
		// Activity earlier than the cohort bucket must not appear anywhere in
		// the row — periods only look forward.
		a := mustNew(t, Config{Cohort: "signup", Activity: "open", Period: "day", Periods: 3})
		a.Feed(ev("open", "u1", -3))
		a.Feed(ev("signup", "u1", 0))
		res := a.Finalize()
		for k, n := range res.Rows[0].Retained {
			if k == 0 {
				continue
			}
			if n != 0 {
				t.Fatalf("period %d retained = %d, want 0", k, n)
			}
		}
	}
	{
		a := mustNew(t, Config{Cohort: "signup", Activity: "open", Period: "day", Periods: 2})
		a.Feed(ev("open", "u1", 0)) // active but never signed up
		res := a.Finalize()
		if len(res.Rows) != 0 {
			t.Fatalf("rows = %+v", res.Rows)
		}
	}
}

func TestRowsSortedByCohort(t *testing.T) {
	a := mustNew(t, Config{Cohort: "signup", Period: "day", Periods: 2})
	a.Feed(ev("signup", "u3", 9))
	a.Feed(ev("signup", "u1", 2))
	a.Feed(ev("signup", "u2", 5))
	res := a.Finalize()
	want := []string{"2026-06-03", "2026-06-06", "2026-06-10"}
	for i, w := range want {
		if res.Rows[i].Cohort != w {
			t.Fatalf("row %d cohort = %q, want %q", i, res.Rows[i].Cohort, w)
		}
	}
}
