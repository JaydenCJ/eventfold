// Package timeq holds every time-shaped query primitive: conversion windows
// ("7d"), date bounds ("2026-06-01"), and day/week bucketing. All arithmetic
// is UTC; weeks start on Monday.
package timeq

import (
	"fmt"
	"strconv"
	"time"
)

// Valid bucketing periods.
const (
	PeriodDay  = "day"
	PeriodWeek = "week"
)

// Range is a half-open query interval [Since, Until). A zero bound means
// unbounded on that side.
type Range struct {
	Since time.Time
	Until time.Time
}

// Contains reports whether t falls inside the range.
func (r Range) Contains(t time.Time) bool {
	if !r.Since.IsZero() && t.Before(r.Since) {
		return false
	}
	if !r.Until.IsZero() && !t.Before(r.Until) {
		return false
	}
	return true
}

// ParseRange builds a Range from --since / --until date strings. Until is an
// inclusive calendar date on the CLI, so it becomes the next UTC midnight
// internally. Empty strings leave the bound open.
func ParseRange(since, until string) (Range, error) {
	var r Range
	if since != "" {
		t, err := ParseDate(since)
		if err != nil {
			return Range{}, err
		}
		r.Since = t
	}
	if until != "" {
		t, err := ParseDate(until)
		if err != nil {
			return Range{}, err
		}
		r.Until = t.AddDate(0, 0, 1)
	}
	if !r.Since.IsZero() && !r.Until.IsZero() && r.Until.Before(r.Since) {
		return Range{}, fmt.Errorf("--until %s is before --since %s", until, since)
	}
	return r, nil
}

// ParseDate parses "2006-01-02" into UTC midnight.
func ParseDate(s string) (time.Time, error) {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, fmt.Errorf("date %q must be YYYY-MM-DD", s)
	}
	return t.UTC(), nil
}

// ParseWindow parses a duration like "30m", "6h", "7d" or "2w". Days and
// weeks are calendar-free fixed spans (24h and 168h) — good enough for
// conversion windows, and unambiguous.
func ParseWindow(s string) (time.Duration, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("window %q must be <number><s|m|h|d|w>", s)
	}
	unit := s[len(s)-1]
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("window %q must be a positive integer plus s|m|h|d|w", s)
	}
	base := map[byte]time.Duration{
		's': time.Second,
		'm': time.Minute,
		'h': time.Hour,
		'd': 24 * time.Hour,
		'w': 7 * 24 * time.Hour,
	}[unit]
	if base == 0 {
		return 0, fmt.Errorf("window %q has unknown unit %q (use s, m, h, d or w)", s, string(unit))
	}
	return time.Duration(n) * base, nil
}

// ValidPeriod reports whether p is a supported bucketing period.
func ValidPeriod(p string) bool {
	return p == PeriodDay || p == PeriodWeek
}

// Bucket truncates t to the start of its UTC day or ISO week (Monday).
func Bucket(t time.Time, period string) time.Time {
	t = t.UTC()
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	if period == PeriodWeek {
		// Monday = 0 offset; Sunday steps back six days.
		offset := (int(day.Weekday()) + 6) % 7
		return day.AddDate(0, 0, -offset)
	}
	return day
}

// AddPeriods advances a bucket start by n periods.
func AddPeriods(bucket time.Time, period string, n int) time.Time {
	if period == PeriodWeek {
		return bucket.AddDate(0, 0, 7*n)
	}
	return bucket.AddDate(0, 0, n)
}

// FormatBucket renders a bucket start for output: the day itself, or the
// Monday a week starts on.
func FormatBucket(bucket time.Time, period string) string {
	return bucket.UTC().Format("2006-01-02")
}

// FormatWindow renders a duration the way users wrote it: whole days, hours
// or minutes when the duration divides evenly, otherwise Go's default form.
func FormatWindow(d time.Duration) string {
	switch {
	case d >= 24*time.Hour && d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	case d >= time.Minute && d%time.Minute == 0:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	default:
		return d.String()
	}
}
