// Package rollup precomputes per-day aggregates so daily count queries never
// re-read raw events.
//
// One JSON file per day partition, e.g. rollups/2026-06-01.json:
//
//	{"schema_version":1,"date":"2026-06-01","source_bytes":8412,
//	 "events":{"signup":{"count":12,"users":9}}}
//
// Freshness is fingerprinted by the source partition's byte size: partitions
// are append-only, so "same size" means "same content" in practice, and any
// append invalidates the rollup automatically.
package rollup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/JaydenCJ/eventfold/internal/event"
	"github.com/JaydenCJ/eventfold/internal/store"
)

// SchemaVersion identifies the rollup file format.
const SchemaVersion = 1

// Agg is the aggregate for one event name on one day.
type Agg struct {
	Count int `json:"count"` // total events
	Users int `json:"users"` // distinct users
}

// Day is one rollup file.
type Day struct {
	SchemaVersion int            `json:"schema_version"`
	Date          string         `json:"date"`
	SourceBytes   int64          `json:"source_bytes"`
	Events        map[string]Agg `json:"events"`
}

// Path returns the rollup file for a day.
func Path(s *store.Store, day string) string {
	return filepath.Join(s.RollupsDir(), day+".json")
}

// Load reads a day's rollup and reports whether it is fresh against the
// current partition size. A missing file returns (nil, false, nil).
func Load(s *store.Store, day string) (*Day, bool, error) {
	raw, err := os.ReadFile(Path(s, day))
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var d Day
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, false, fmt.Errorf("rollup %s: %v", day, err)
	}
	if d.SchemaVersion != SchemaVersion {
		return &d, false, nil
	}
	size, err := s.DaySize(day)
	if err != nil {
		return nil, false, err
	}
	return &d, d.SourceBytes == size, nil
}

// BuildDay recomputes one day's rollup from raw events and writes it
// atomically (temp file + rename).
func BuildDay(s *store.Store, day string) (*Day, error) {
	size, err := s.DaySize(day)
	if err != nil {
		return nil, err
	}
	d := &Day{SchemaVersion: SchemaVersion, Date: day, SourceBytes: size, Events: map[string]Agg{}}
	users := map[string]map[string]bool{}
	_, err = s.ScanDay(day, store.ScanQuery{}, func(e event.Event) error {
		agg := d.Events[e.Name]
		agg.Count++
		if users[e.Name] == nil {
			users[e.Name] = map[string]bool{}
		}
		users[e.Name][e.User] = true
		agg.Users = len(users[e.Name])
		d.Events[e.Name] = agg
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.RollupsDir(), 0o755); err != nil {
		return nil, err
	}
	raw, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return nil, err
	}
	tmp := Path(s, day) + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return nil, err
	}
	return d, os.Rename(tmp, Path(s, day))
}

// BuildStats reports one Build run.
type BuildStats struct {
	Built []string // days recomputed
	Fresh []string // days skipped because the rollup was already fresh
}

// Build refreshes rollups for every day partition. With force, fresh days
// are rebuilt anyway.
func Build(s *store.Store, force bool) (BuildStats, error) {
	var stats BuildStats
	days, err := s.Days()
	if err != nil {
		return stats, err
	}
	for _, day := range days {
		if !force {
			if _, fresh, err := Load(s, day); err != nil {
				return stats, err
			} else if fresh {
				stats.Fresh = append(stats.Fresh, day)
				continue
			}
		}
		if _, err := BuildDay(s, day); err != nil {
			return stats, fmt.Errorf("day %s: %w", day, err)
		}
		stats.Built = append(stats.Built, day)
	}
	return stats, nil
}
