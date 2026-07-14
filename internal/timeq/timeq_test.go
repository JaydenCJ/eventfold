// Tests for time-query primitives: windows, date ranges, and day/week
// bucketing. Weeks start on Monday, everything is UTC.
package timeq

import (
	"testing"
	"time"
)

func TestParseWindowUnits(t *testing.T) {
	cases := map[string]time.Duration{
		"45s": 45 * time.Second,
		"30m": 30 * time.Minute,
		"6h":  6 * time.Hour,
		"7d":  7 * 24 * time.Hour,
		"2w":  14 * 24 * time.Hour,
	}
	for in, want := range cases {
		got, err := ParseWindow(in)
		if err != nil {
			t.Errorf("ParseWindow(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseWindow(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseWindowRejections(t *testing.T) {
	for _, in := range []string{"", "d", "7", "7x", "-3d", "0d", "1.5h", "d7"} {
		if _, err := ParseWindow(in); err == nil {
			t.Errorf("ParseWindow(%q) should fail", in)
		}
	}
}

func TestFormatWindow(t *testing.T) {
	cases := map[string]time.Duration{
		"7d":  7 * 24 * time.Hour,
		"6h":  6 * time.Hour,
		"30m": 30 * time.Minute,
		"45s": 45 * time.Second,
		"90m": 90 * time.Minute, // non-whole hours fall back to minutes
	}
	for want, d := range cases {
		if got := FormatWindow(d); got != want {
			t.Errorf("FormatWindow(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestParseDate(t *testing.T) {
	got, err := ParseDate("2026-06-15")
	if err != nil {
		t.Fatalf("ParseDate: %v", err)
	}
	want := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("ParseDate = %v, want %v", got, want)
	}
	if _, err := ParseDate("15/06/2026"); err == nil {
		t.Fatalf("non-ISO date should fail")
	}
}

func TestParseRangeSemantics(t *testing.T) {
	{
		r, err := ParseRange("2026-06-01", "2026-06-03")
		if err != nil {
			t.Fatalf("ParseRange: %v", err)
		}
		// 23:59:59 on the until day is still inside the range.
		if !r.Contains(time.Date(2026, 6, 3, 23, 59, 59, 0, time.UTC)) {
			t.Errorf("end of until-day should be contained")
		}
		// Midnight of the next day is out.
		if r.Contains(time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("day after until should be excluded")
		}
		if !r.Contains(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("since midnight should be contained")
		}
		if r.Contains(time.Date(2026, 5, 31, 23, 59, 59, 0, time.UTC)) {
			t.Errorf("before since should be excluded")
		}
	}
	{
		r, err := ParseRange("", "")
		if err != nil {
			t.Fatalf("ParseRange: %v", err)
		}
		if !r.Contains(time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC)) ||
			!r.Contains(time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("open range should contain everything")
		}
	}
	{
		if _, err := ParseRange("2026-06-10", "2026-06-01"); err == nil {
			t.Fatalf("until before since should fail")
		}
	}
}

func TestBucketing(t *testing.T) {
	{
		in := time.Date(2026, 6, 3, 17, 45, 12, 0, time.UTC)
		want := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)
		if got := Bucket(in, PeriodDay); !got.Equal(want) {
			t.Fatalf("Bucket(day) = %v, want %v", got, want)
		}
	}
	{
		// 2026-06-03 is a Wednesday; its week starts Monday 2026-06-01.
		monday := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
		for day := 1; day <= 7; day++ {
			in := time.Date(2026, 6, day, 12, 0, 0, 0, time.UTC)
			if got := Bucket(in, PeriodWeek); !got.Equal(monday) {
				t.Errorf("Bucket(2026-06-%02d, week) = %v, want %v", day, got, monday)
			}
		}
		// Sunday 2026-06-07 still belongs to the Monday-06-01 week; Monday
		// 2026-06-08 starts the next one.
		next := time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC)
		if got := Bucket(next.Add(time.Hour), PeriodWeek); !got.Equal(next) {
			t.Errorf("next Monday bucket = %v, want %v", got, next)
		}
	}
}

func TestPeriodHelpers(t *testing.T) {
	{
		base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
		if got := AddPeriods(base, PeriodDay, 3); !got.Equal(base.AddDate(0, 0, 3)) {
			t.Errorf("AddPeriods(day,3) = %v", got)
		}
		if got := AddPeriods(base, PeriodWeek, 2); !got.Equal(base.AddDate(0, 0, 14)) {
			t.Errorf("AddPeriods(week,2) = %v", got)
		}
	}
	{
		if !ValidPeriod("day") || !ValidPeriod("week") {
			t.Errorf("day and week must be valid")
		}
		for _, p := range []string{"month", "", "Day", "hour"} {
			if ValidPeriod(p) {
				t.Errorf("%q should be invalid", p)
			}
		}
	}
}
