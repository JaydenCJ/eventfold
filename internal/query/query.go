// Package query executes analytics queries against a store. It is the single
// shared brain behind both the CLI and the JSON API, so a curl and a
// terminal session can never disagree.
package query

import (
	"sort"

	"github.com/JaydenCJ/eventfold/internal/event"
	"github.com/JaydenCJ/eventfold/internal/funnel"
	"github.com/JaydenCJ/eventfold/internal/retention"
	"github.com/JaydenCJ/eventfold/internal/rollup"
	"github.com/JaydenCJ/eventfold/internal/store"
	"github.com/JaydenCJ/eventfold/internal/timeq"
)

// Funnel scans the range and computes an ordered conversion funnel.
func Funnel(st *store.Store, cfg funnel.Config, r timeq.Range) (funnel.Result, store.ScanStats, error) {
	an, err := funnel.New(cfg)
	if err != nil {
		return funnel.Result{}, store.ScanStats{}, err
	}
	names := map[string]bool{}
	for _, s := range cfg.Steps {
		names[s] = true
	}
	stats, err := st.Scan(store.ScanQuery{Names: names, Range: r}, func(e event.Event) error {
		an.Feed(e)
		return nil
	})
	if err != nil {
		return funnel.Result{}, stats, err
	}
	return an.Finalize(), stats, nil
}

// Retention scans the range and computes a cohort retention triangle.
func Retention(st *store.Store, cfg retention.Config, r timeq.Range) (retention.Result, store.ScanStats, error) {
	an, err := retention.New(cfg)
	if err != nil {
		return retention.Result{}, store.ScanStats{}, err
	}
	var names map[string]bool
	if cfg.Activity != "" {
		names = map[string]bool{cfg.Cohort: true, cfg.Activity: true}
	}
	stats, err := st.Scan(store.ScanQuery{Names: names, Range: r}, func(e event.Event) error {
		an.Feed(e)
		return nil
	})
	if err != nil {
		return retention.Result{}, stats, err
	}
	return an.Finalize(), stats, nil
}

// Bucket is one time bucket of a count query.
type Bucket struct {
	Start      string `json:"start"` // bucket start day, "2006-01-02"
	Count      int    `json:"count"`
	Users      int    `json:"users"`
	FromRollup bool   `json:"from_rollup"` // true when served from a fresh rollup file
}

// Count buckets one event's volume over time. Day buckets are served from
// fresh rollup files when available (falling back to a raw scan per day);
// week buckets always scan, because distinct users cannot be summed across
// daily rollups without double counting.
func Count(st *store.Store, name string, by string, r timeq.Range) ([]Bucket, error) {
	if by == timeq.PeriodDay {
		return countDaily(st, name, r)
	}
	return countScan(st, name, by, r)
}

func countDaily(st *store.Store, name string, r timeq.Range) ([]Bucket, error) {
	days, err := st.DaysIn(r)
	if err != nil {
		return nil, err
	}
	buckets := make([]Bucket, 0, len(days))
	for _, day := range days {
		b := Bucket{Start: day}
		if d, fresh, err := rollup.Load(st, day); err != nil {
			return nil, err
		} else if fresh && wholeDayInRange(day, r) {
			agg := d.Events[name]
			b.Count, b.Users, b.FromRollup = agg.Count, agg.Users, true
		} else {
			users := map[string]bool{}
			_, err := st.ScanDay(day, store.ScanQuery{Names: map[string]bool{name: true}, Range: r},
				func(e event.Event) error {
					b.Count++
					users[e.User] = true
					return nil
				})
			if err != nil {
				return nil, err
			}
			b.Users = len(users)
		}
		buckets = append(buckets, b)
	}
	return buckets, nil
}

// wholeDayInRange reports whether a rollup (which always covers the full
// day) can answer for this day, i.e. no range bound cuts into it.
func wholeDayInRange(day string, r timeq.Range) bool {
	start, err := timeq.ParseDate(day)
	if err != nil {
		return false
	}
	end := start.AddDate(0, 0, 1)
	if !r.Since.IsZero() && r.Since.After(start) {
		return false
	}
	if !r.Until.IsZero() && r.Until.Before(end) {
		return false
	}
	return true
}

func countScan(st *store.Store, name string, by string, r timeq.Range) ([]Bucket, error) {
	counts := map[string]int{}
	users := map[string]map[string]bool{}
	_, err := st.Scan(store.ScanQuery{Names: map[string]bool{name: true}, Range: r},
		func(e event.Event) error {
			b := timeq.FormatBucket(timeq.Bucket(e.Time, by), by)
			counts[b]++
			if users[b] == nil {
				users[b] = map[string]bool{}
			}
			users[b][e.User] = true
			return nil
		})
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	buckets := make([]Bucket, 0, len(keys))
	for _, k := range keys {
		buckets = append(buckets, Bucket{Start: k, Count: counts[k], Users: len(users[k])})
	}
	return buckets, nil
}

// EventAgg is one distinct event name with totals over a range.
type EventAgg struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
	Users int    `json:"users"`
}

// Events lists distinct event names with exact totals, sorted by count
// descending then name. It always scans: distinct-user totals across days
// cannot come from daily rollups.
func Events(st *store.Store, r timeq.Range) ([]EventAgg, store.ScanStats, error) {
	counts := map[string]int{}
	users := map[string]map[string]bool{}
	stats, err := st.Scan(store.ScanQuery{Range: r}, func(e event.Event) error {
		counts[e.Name]++
		if users[e.Name] == nil {
			users[e.Name] = map[string]bool{}
		}
		users[e.Name][e.User] = true
		return nil
	})
	if err != nil {
		return nil, stats, err
	}
	out := make([]EventAgg, 0, len(counts))
	for name, c := range counts {
		out = append(out, EventAgg{Name: name, Count: c, Users: len(users[name])})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out, stats, nil
}
