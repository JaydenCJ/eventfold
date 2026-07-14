// Package store persists events as day-partitioned NDJSON files.
//
// Layout under the data directory:
//
//	events/2026-06-01.ndjson   one file per UTC day, append-only
//	rollups/2026-06-01.json    precomputed per-day aggregates (see rollup)
//
// Files are plain NDJSON on purpose: they stay greppable, diffable and
// owned by the user. The store guarantees idempotent ingestion for events
// that carry an "id", and tolerates (but counts) malformed lines on scan.
package store

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/JaydenCJ/eventfold/internal/event"
	"github.com/JaydenCJ/eventfold/internal/timeq"
)

const eventsExt = ".ndjson"

// Store is a handle on one data directory.
type Store struct {
	dir string
}

// Open validates the path and returns a store. The directory tree is created
// lazily on first append, so read-only commands never litter the filesystem.
func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("data directory must not be empty")
	}
	if fi, err := os.Stat(dir); err == nil && !fi.IsDir() {
		return nil, fmt.Errorf("%s exists and is not a directory", dir)
	}
	return &Store{dir: dir}, nil
}

// Dir returns the data directory root.
func (s *Store) Dir() string { return s.dir }

// EventsDir returns the day-partition directory.
func (s *Store) EventsDir() string { return filepath.Join(s.dir, "events") }

// RollupsDir returns the rollup directory.
func (s *Store) RollupsDir() string { return filepath.Join(s.dir, "rollups") }

// DayPath returns the partition file for a "2006-01-02" day.
func (s *Store) DayPath(day string) string {
	return filepath.Join(s.EventsDir(), day+eventsExt)
}

// Days lists existing day partitions, sorted ascending. Files that do not
// look like day partitions are ignored.
func (s *Store) Days() ([]string, error) {
	entries, err := os.ReadDir(s.EventsDir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var days []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, eventsExt) {
			continue
		}
		day := strings.TrimSuffix(name, eventsExt)
		if _, err := time.Parse("2006-01-02", day); err != nil {
			continue
		}
		days = append(days, day)
	}
	sort.Strings(days)
	return days, nil
}

// DaysIn filters existing partitions to those that can intersect r.
func (s *Store) DaysIn(r timeq.Range) ([]string, error) {
	days, err := s.Days()
	if err != nil {
		return nil, err
	}
	out := days[:0]
	for _, day := range days {
		start, _ := timeq.ParseDate(day)
		end := start.AddDate(0, 0, 1)
		if !r.Since.IsZero() && !end.After(r.Since) {
			continue
		}
		if !r.Until.IsZero() && !start.Before(r.Until) {
			continue
		}
		out = append(out, day)
	}
	return out, nil
}

// AppendStats reports what one Append call did.
type AppendStats struct {
	Written    int            // events persisted
	Duplicates int            // events skipped because their id already exists
	Days       map[string]int // written events per day partition
}

// Append persists a batch, deduplicating by id against both the batch itself
// and each target partition. Events without an id are always written.
func (s *Store) Append(events []event.Event) (AppendStats, error) {
	stats := AppendStats{Days: map[string]int{}}
	if len(events) == 0 {
		return stats, nil
	}
	byDay := map[string][]event.Event{}
	for _, e := range events {
		byDay[e.Day()] = append(byDay[e.Day()], e)
	}
	if err := os.MkdirAll(s.EventsDir(), 0o755); err != nil {
		return stats, err
	}
	days := make([]string, 0, len(byDay))
	for day := range byDay {
		days = append(days, day)
	}
	sort.Strings(days)
	for _, day := range days {
		written, dups, err := s.appendDay(day, byDay[day])
		if err != nil {
			return stats, fmt.Errorf("day %s: %w", day, err)
		}
		stats.Written += written
		stats.Duplicates += dups
		if written > 0 {
			stats.Days[day] = written
		}
	}
	return stats, nil
}

func (s *Store) appendDay(day string, batch []event.Event) (written, dups int, err error) {
	seen, err := s.existingIDs(day, batch)
	if err != nil {
		return 0, 0, err
	}
	var buf strings.Builder
	for _, e := range batch {
		if e.ID != "" {
			if seen[e.ID] {
				dups++
				continue
			}
			seen[e.ID] = true
		}
		line, err := e.MarshalLine()
		if err != nil {
			return written, dups, err
		}
		buf.Write(line)
		buf.WriteByte('\n')
		written++
	}
	if buf.Len() == 0 {
		return 0, dups, nil
	}
	f, err := os.OpenFile(s.DayPath(day), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, dups, err
	}
	if _, err := f.WriteString(buf.String()); err != nil {
		f.Close()
		return 0, dups, err
	}
	return written, dups, f.Close()
}

// existingIDs collects ids already stored in a partition — but only when the
// incoming batch actually carries ids, so id-free ingestion never re-reads
// history.
func (s *Store) existingIDs(day string, batch []event.Event) (map[string]bool, error) {
	seen := map[string]bool{}
	hasIDs := false
	for _, e := range batch {
		if e.ID != "" {
			hasIDs = true
			break
		}
	}
	if !hasIDs {
		return seen, nil
	}
	f, err := os.Open(s.DayPath(day))
	if os.IsNotExist(err) {
		return seen, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := newLineScanner(f)
	for sc.Scan() {
		e, err := event.ParseLine(sc.Bytes())
		if err != nil {
			continue // malformed history is scan's problem, not append's
		}
		if e.ID != "" {
			seen[e.ID] = true
		}
	}
	return seen, sc.Err()
}

// ScanQuery narrows a scan to event names and a time range.
type ScanQuery struct {
	Names map[string]bool // nil or empty = all events
	Range timeq.Range
}

func (q ScanQuery) match(e event.Event) bool {
	if len(q.Names) > 0 && !q.Names[e.Name] {
		return false
	}
	return q.Range.Contains(e.Time)
}

// ScanStats counts what a scan saw.
type ScanStats struct {
	Lines   int // NDJSON lines read
	Matched int // events delivered to the callback
	Skipped int // malformed lines tolerated
}

// Scan streams matching events, one callback per event, in file order within
// each day and day order across days. Malformed lines are counted, never
// fatal: an analytics query should not die because one line was hand-edited.
func (s *Store) Scan(q ScanQuery, fn func(event.Event) error) (ScanStats, error) {
	var stats ScanStats
	days, err := s.DaysIn(q.Range)
	if err != nil {
		return stats, err
	}
	for _, day := range days {
		if err := s.scanDay(day, q, &stats, fn); err != nil {
			return stats, fmt.Errorf("day %s: %w", day, err)
		}
	}
	return stats, nil
}

// ScanDay streams one partition with the same tolerance rules as Scan.
func (s *Store) ScanDay(day string, q ScanQuery, fn func(event.Event) error) (ScanStats, error) {
	var stats ScanStats
	if err := s.scanDay(day, q, &stats, fn); err != nil {
		return stats, fmt.Errorf("day %s: %w", day, err)
	}
	return stats, nil
}

func (s *Store) scanDay(day string, q ScanQuery, stats *ScanStats, fn func(event.Event) error) error {
	f, err := os.Open(s.DayPath(day))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := newLineScanner(f)
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		stats.Lines++
		e, err := event.ParseLine(line)
		if err != nil {
			stats.Skipped++
			continue
		}
		if !q.match(e) {
			continue
		}
		stats.Matched++
		if err := fn(e); err != nil {
			return err
		}
	}
	return sc.Err()
}

// DaySize returns the byte size of a partition file (0 if absent) — the
// freshness fingerprint used by rollups.
func (s *Store) DaySize(day string) (int64, error) {
	fi, err := os.Stat(s.DayPath(day))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

func newLineScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), event.MaxLineSize+2)
	return sc
}
