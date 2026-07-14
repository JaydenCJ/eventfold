// Tests for the funnel engine. The cases that matter most are the ones
// naive implementations get wrong: out-of-order feeds, window edges,
// re-anchoring on a later first step, and repeated step names.
package funnel

import (
	"testing"
	"time"

	"github.com/JaydenCJ/eventfold/internal/event"
)

var base = time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)

// ev builds a step event at base + minutes for a user.
func ev(name, user string, minutes int) event.Event {
	return event.Event{Name: name, User: user, Time: base.Add(time.Duration(minutes) * time.Minute)}
}

func evProp(name, user string, minutes int, key, val string) event.Event {
	e := ev(name, user, minutes)
	e.Props = map[string]string{key: val}
	return e
}

func mustNew(t *testing.T, cfg Config) *Analyzer {
	t.Helper()
	a, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

func feed(a *Analyzer, events ...event.Event) {
	for _, e := range events {
		a.Feed(e)
	}
}

func TestConfigValidate(t *testing.T) {
	valid := Config{Steps: []string{"a", "b"}, Window: time.Hour}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	bad := []Config{
		{Steps: []string{"a"}, Window: time.Hour},
		{Steps: []string{"a", ""}, Window: time.Hour},
		{Steps: []string{"a", "b"}, Window: 0},
		{Steps: []string{"a", "b"}, Window: -time.Hour},
		{Steps: make([]string, MaxSteps+1), Window: time.Hour},
	}
	for i, cfg := range bad {
		if err := cfg.Validate(); err == nil {
			t.Errorf("case %d should be rejected: %+v", i, cfg)
		}
	}
}

func TestOrderedConversion(t *testing.T) {
	{
		a := mustNew(t, Config{Steps: []string{"signup", "activate", "pay"}, Window: 24 * time.Hour})
		feed(a,
			ev("signup", "u1", 0), ev("activate", "u1", 10), ev("pay", "u1", 60),
			ev("signup", "u2", 0), ev("activate", "u2", 30),
			ev("signup", "u3", 0),
		)
		res := a.Finalize()
		if res.Entered != 3 {
			t.Fatalf("entered = %d", res.Entered)
		}
		want := []int{3, 2, 1}
		for i, n := range want {
			if res.Steps[i].Users != n {
				t.Errorf("step %d users = %d, want %d", i, res.Steps[i].Users, n)
			}
		}
		if res.Steps[2].PctOfFirst != 100.0/3 {
			t.Errorf("pct of first = %v", res.Steps[2].PctOfFirst)
		}
		if res.Steps[2].PctOfPrev != 50 {
			t.Errorf("pct of prev = %v", res.Steps[2].PctOfPrev)
		}
	}
	{
		// u1 pays before activating: presence of all events is not conversion.
		a := mustNew(t, Config{Steps: []string{"signup", "activate", "pay"}, Window: 24 * time.Hour})
		feed(a, ev("signup", "u1", 0), ev("pay", "u1", 5), ev("activate", "u1", 10))
		res := a.Finalize()
		if res.Steps[1].Users != 1 || res.Steps[2].Users != 0 {
			t.Fatalf("steps = %+v", res.Steps)
		}
	}
}

func TestFeedOrderAndTimestampTies(t *testing.T) {
	{
		// Events arrive shuffled across partitions; results must match sorted feed.
		a := mustNew(t, Config{Steps: []string{"a", "b", "c"}, Window: time.Hour})
		feed(a, ev("c", "u1", 30), ev("a", "u1", 0), ev("b", "u1", 10))
		res := a.Finalize()
		if res.Steps[2].Users != 1 {
			t.Fatalf("shuffled feed lost the conversion: %+v", res.Steps)
		}
	}
	{
		// Same-second events are common with second-precision SDKs; ties break
		// by feed order, so a→b at the same instant still converts.
		a := mustNew(t, Config{Steps: []string{"a", "b"}, Window: time.Hour})
		feed(a, ev("a", "u1", 0), ev("b", "u1", 0))
		res := a.Finalize()
		if res.Steps[1].Users != 1 {
			t.Fatalf("same-timestamp conversion missed: %+v", res.Steps)
		}
	}
}

func TestWindowEdges(t *testing.T) {
	{
		a := mustNew(t, Config{Steps: []string{"a", "b"}, Window: time.Hour})
		feed(a, ev("a", "u1", 0), ev("b", "u1", 61)) // one minute too late
		res := a.Finalize()
		if res.Steps[1].Users != 0 {
			t.Fatalf("late step counted: %+v", res.Steps)
		}
	}
	{
		a := mustNew(t, Config{Steps: []string{"a", "b"}, Window: time.Hour})
		feed(a, ev("a", "u1", 0), ev("b", "u1", 60)) // exactly on the deadline
		res := a.Finalize()
		if res.Steps[1].Users != 1 {
			t.Fatalf("boundary step must count: %+v", res.Steps)
		}
	}
}

func TestReanchoringOnLaterFirstStep(t *testing.T) {
	// u1 signs up in January-equivalent (minute 0), goes dormant, then signs
	// up again and converts. First-occurrence-only funnels miss this user.
	a := mustNew(t, Config{Steps: []string{"signup", "pay"}, Window: time.Hour})
	feed(a,
		ev("signup", "u1", 0),
		ev("signup", "u1", 300),
		ev("pay", "u1", 330),
	)
	res := a.Finalize()
	if res.Steps[1].Users != 1 {
		t.Fatalf("re-anchored conversion missed: %+v", res.Steps)
	}
}

func TestRepeatedStepNames(t *testing.T) {
	// pageview → pageview measures "came back for a second view".
	a := mustNew(t, Config{Steps: []string{"pageview", "pageview"}, Window: time.Hour})
	feed(a, ev("pageview", "u1", 0), ev("pageview", "u1", 5), ev("pageview", "u2", 0))
	res := a.Finalize()
	if res.Steps[0].Users != 2 || res.Steps[1].Users != 1 {
		t.Fatalf("repeated-step funnel = %+v", res.Steps)
	}
}

func TestIrrelevantEventsIgnored(t *testing.T) {
	{
		a := mustNew(t, Config{Steps: []string{"a", "b"}, Window: time.Hour})
		feed(a, ev("noise", "u1", 0), ev("a", "u1", 1), ev("noise", "u1", 2), ev("b", "u1", 3))
		res := a.Finalize()
		if res.Entered != 1 || res.Steps[1].Users != 1 {
			t.Fatalf("noise affected funnel: %+v", res)
		}
	}
	{
		a := mustNew(t, Config{Steps: []string{"a", "b"}, Window: time.Hour})
		feed(a, ev("b", "u1", 0), ev("b", "u1", 10))
		res := a.Finalize()
		if res.Entered != 0 || res.Steps[0].Users != 0 || res.Steps[1].Users != 0 {
			t.Fatalf("user without step 0 entered: %+v", res)
		}
	}
}

func TestMedianTimeToStep(t *testing.T) {
	{
		a := mustNew(t, Config{Steps: []string{"a", "b"}, Window: 2 * time.Hour})
		// Conversion times: 10, 20, 90 minutes → median 20 minutes.
		feed(a,
			ev("a", "u1", 0), ev("b", "u1", 10),
			ev("a", "u2", 0), ev("b", "u2", 20),
			ev("a", "u3", 0), ev("b", "u3", 90),
		)
		res := a.Finalize()
		if got := res.Steps[1].MedianSecs; got != 20*60 {
			t.Fatalf("median = %v seconds, want 1200", got)
		}
		if res.Steps[1].MedianCount != 3 {
			t.Fatalf("median sample size = %d", res.Steps[1].MedianCount)
		}
	}
	{
		a := mustNew(t, Config{Steps: []string{"a", "b"}, Window: 2 * time.Hour})
		feed(a,
			ev("a", "u1", 0), ev("b", "u1", 10),
			ev("a", "u2", 0), ev("b", "u2", 40),
		)
		res := a.Finalize()
		// Lower middle of {10m, 40m} is 10m: always a duration a real user produced.
		if got := res.Steps[1].MedianSecs; got != 10*60 {
			t.Fatalf("median = %v seconds, want 600", got)
		}
	}
}

func TestSegmentationByFirstStepProperty(t *testing.T) {
	a := mustNew(t, Config{Steps: []string{"signup", "pay"}, Window: time.Hour, By: "plan"})
	feed(a,
		evProp("signup", "u1", 0, "plan", "pro"), ev("pay", "u1", 10),
		evProp("signup", "u2", 0, "plan", "pro"),
		evProp("signup", "u3", 0, "plan", "free"),
		ev("signup", "u4", 0), // no plan prop → "(none)" segment
	)
	res := a.Finalize()
	if len(res.Segments) != 3 {
		t.Fatalf("segments = %+v", res.Segments)
	}
	// Sorted by entered desc, then key: pro (2), then (none) and free (1 each).
	if res.Segments[0].Key != "pro" || res.Segments[0].Entered != 2 {
		t.Fatalf("first segment = %+v", res.Segments[0])
	}
	if res.Segments[1].Key != "(none)" || res.Segments[2].Key != "free" {
		t.Fatalf("segment order = %q, %q", res.Segments[1].Key, res.Segments[2].Key)
	}
	if res.Segments[0].Steps[1].Users != 1 {
		t.Fatalf("pro conversions = %+v", res.Segments[0].Steps)
	}
}

func TestEmptyInputAndDeterminism(t *testing.T) {
	{
		a := mustNew(t, Config{Steps: []string{"a", "b"}, Window: time.Hour})
		res := a.Finalize()
		if res.Entered != 0 || len(res.Steps) != 2 {
			t.Fatalf("empty funnel = %+v", res)
		}
		if res.Steps[0].PctOfFirst != 0 {
			t.Fatalf("empty funnel pct should be 0, got %v", res.Steps[0].PctOfFirst)
		}
	}
	{
		build := func() Result {
			a := mustNew(t, Config{Steps: []string{"a", "b"}, Window: time.Hour, By: "k"})
			for i := 0; i < 50; i++ {
				u := string(rune('a' + i%7))
				feed(a, evProp("a", u, i, "k", u), ev("b", u, i+1))
			}
			return a.Finalize()
		}
		first := build()
		for i := 0; i < 5; i++ {
			next := build()
			if len(next.Segments) != len(first.Segments) {
				t.Fatalf("segment count varies")
			}
			for j := range next.Segments {
				if next.Segments[j].Key != first.Segments[j].Key {
					t.Fatalf("segment order varies: %v vs %v", next.Segments[j], first.Segments[j])
				}
			}
		}
	}
}
