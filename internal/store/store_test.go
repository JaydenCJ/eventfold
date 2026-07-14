// Tests for the day-partitioned NDJSON store: partition layout, idempotent
// appends, tolerant scans, and range narrowing.
package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JaydenCJ/eventfold/internal/event"
	"github.com/JaydenCJ/eventfold/internal/timeq"
)

// ev builds a test event at an offset from a fixed base time.
func ev(name, user string, dayOffset int, hour int) event.Event {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	return event.Event{
		Name: name,
		User: user,
		Time: base.AddDate(0, 0, dayOffset).Add(time.Duration(hour) * time.Hour),
	}
}

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestOpenRejectsEmptyAndFilePaths(t *testing.T) {
	if _, err := Open(""); err == nil {
		t.Fatalf("empty dir should fail")
	}
	f := filepath.Join(t.TempDir(), "plain-file")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(f); err == nil {
		t.Fatalf("file path should fail")
	}
}

func TestAppendPartitionsByUTCDay(t *testing.T) {
	s := openTemp(t)
	stats, err := s.Append([]event.Event{
		ev("a", "u1", 0, 10),
		ev("a", "u2", 1, 10),
		ev("b", "u1", 1, 11),
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if stats.Written != 3 {
		t.Fatalf("written = %d, want 3", stats.Written)
	}
	if stats.Days["2026-06-01"] != 1 || stats.Days["2026-06-02"] != 2 {
		t.Fatalf("day split = %v", stats.Days)
	}
	days, err := s.Days()
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 2 || days[0] != "2026-06-01" || days[1] != "2026-06-02" {
		t.Fatalf("Days() = %v", days)
	}
}

func TestAppendDeduplicationByID(t *testing.T) {
	{
		s := openTemp(t)
		e1 := ev("a", "u1", 0, 1)
		e1.ID = "k1"
		e2 := ev("a", "u1", 0, 2)
		e2.ID = "k1" // same key, later timestamp: must be dropped
		stats, err := s.Append([]event.Event{e1, e2})
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		if stats.Written != 1 || stats.Duplicates != 1 {
			t.Fatalf("stats = %+v", stats)
		}
	}
	{
		// Re-ingesting the same export twice must be a no-op the second time —
		// the property that makes `eventfold ingest` safe to re-run.
		s := openTemp(t)
		e := ev("a", "u1", 0, 1)
		e.ID = "stable-key"
		if _, err := s.Append([]event.Event{e}); err != nil {
			t.Fatal(err)
		}
		stats, err := s.Append([]event.Event{e})
		if err != nil {
			t.Fatal(err)
		}
		if stats.Written != 0 || stats.Duplicates != 1 {
			t.Fatalf("second append stats = %+v", stats)
		}
		var count int
		if _, err := s.Scan(ScanQuery{}, func(event.Event) error { count++; return nil }); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("stored events = %d, want 1", count)
		}
	}
	{
		s := openTemp(t)
		e := ev("a", "u1", 0, 1)
		for i := 0; i < 3; i++ {
			if _, err := s.Append([]event.Event{e}); err != nil {
				t.Fatal(err)
			}
		}
		var count int
		if _, err := s.Scan(ScanQuery{}, func(event.Event) error { count++; return nil }); err != nil {
			t.Fatal(err)
		}
		if count != 3 {
			t.Fatalf("stored events = %d, want 3 (no ids, no dedup)", count)
		}
	}
}

func TestScanFilterAndOrder(t *testing.T) {
	{
		s := openTemp(t)
		if _, err := s.Append([]event.Event{
			ev("signup", "u1", 0, 10),
			ev("pageview", "u1", 0, 11),
			ev("signup", "u2", 5, 10),
		}); err != nil {
			t.Fatal(err)
		}
		r, _ := timeq.ParseRange("2026-06-01", "2026-06-03")
		var got []string
		stats, err := s.Scan(ScanQuery{Names: map[string]bool{"signup": true}, Range: r},
			func(e event.Event) error {
				got = append(got, e.User)
				return nil
			})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0] != "u1" {
			t.Fatalf("matched users = %v", got)
		}
		if stats.Matched != 1 {
			t.Fatalf("stats = %+v", stats)
		}
	}
	{
		s := openTemp(t)
		if _, err := s.Append([]event.Event{
			ev("a", "u3", 1, 1),
			ev("a", "u1", 0, 1),
			ev("a", "u2", 0, 2),
		}); err != nil {
			t.Fatal(err)
		}
		var got []string
		if _, err := s.Scan(ScanQuery{}, func(e event.Event) error {
			got = append(got, e.User)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if strings.Join(got, ",") != "u1,u2,u3" {
			t.Fatalf("scan order = %v", got)
		}
	}
}

func TestScanToleratesMalformedLines(t *testing.T) {
	s := openTemp(t)
	if _, err := s.Append([]event.Event{ev("a", "u1", 0, 1)}); err != nil {
		t.Fatal(err)
	}
	// Simulate a hand-edited partition: garbage line plus a blank line.
	f, err := os.OpenFile(s.DayPath("2026-06-01"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("this is not json\n\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	var count int
	stats, err := s.Scan(ScanQuery{}, func(event.Event) error { count++; return nil })
	if err != nil {
		t.Fatalf("Scan should tolerate garbage: %v", err)
	}
	if count != 1 || stats.Skipped != 1 {
		t.Fatalf("count = %d, stats = %+v", count, stats)
	}
}

func TestDayListing(t *testing.T) {
	{
		s := openTemp(t)
		if _, err := s.Append([]event.Event{ev("a", "u1", 0, 1)}); err != nil {
			t.Fatal(err)
		}
		for _, junk := range []string{"notes.txt", "2026-06-01.ndjson.bak", "junk.ndjson"} {
			if err := os.WriteFile(filepath.Join(s.EventsDir(), junk), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		days, err := s.Days()
		if err != nil {
			t.Fatal(err)
		}
		if len(days) != 1 || days[0] != "2026-06-01" {
			t.Fatalf("Days() = %v", days)
		}
	}
	{
		s := openTemp(t)
		if _, err := s.Append([]event.Event{
			ev("a", "u1", 0, 1),  // 06-01
			ev("a", "u1", 3, 1),  // 06-04
			ev("a", "u1", 10, 1), // 06-11
		}); err != nil {
			t.Fatal(err)
		}
		r, _ := timeq.ParseRange("2026-06-02", "2026-06-05")
		days, err := s.DaysIn(r)
		if err != nil {
			t.Fatal(err)
		}
		if len(days) != 1 || days[0] != "2026-06-04" {
			t.Fatalf("DaysIn = %v", days)
		}
	}
}

func TestScanOnEmptyStoreIsClean(t *testing.T) {
	s := openTemp(t)
	stats, err := s.Scan(ScanQuery{}, func(event.Event) error {
		t.Fatal("callback must not fire")
		return nil
	})
	if err != nil {
		t.Fatalf("Scan on empty store: %v", err)
	}
	if stats.Lines != 0 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestDaySizeReflectsAppends(t *testing.T) {
	s := openTemp(t)
	if size, err := s.DaySize("2026-06-01"); err != nil || size != 0 {
		t.Fatalf("missing partition: size=%d err=%v", size, err)
	}
	if _, err := s.Append([]event.Event{ev("a", "u1", 0, 1)}); err != nil {
		t.Fatal(err)
	}
	before, _ := s.DaySize("2026-06-01")
	if before == 0 {
		t.Fatalf("size should grow after append")
	}
	if _, err := s.Append([]event.Event{ev("b", "u2", 0, 2)}); err != nil {
		t.Fatal(err)
	}
	after, _ := s.DaySize("2026-06-01")
	if after <= before {
		t.Fatalf("size %d should exceed %d after second append", after, before)
	}
}
