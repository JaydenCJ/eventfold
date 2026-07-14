// Package retention computes cohort retention triangles.
//
// Users are grouped into cohorts by the day or week of their *first* cohort
// event (e.g. "signup"). For each later period k, a cohort member is
// retained when they performed the activity event (or any event, when
// unset) inside period k counted from the cohort bucket. Period 0 is the
// cohort bucket itself.
package retention

import (
	"fmt"
	"sort"
	"time"

	"github.com/JaydenCJ/eventfold/internal/event"
	"github.com/JaydenCJ/eventfold/internal/timeq"
)

// MaxPeriods bounds triangle width; wider is unreadable and almost always
// a typo.
const MaxPeriods = 52

// Config describes a retention query.
type Config struct {
	Cohort   string // cohort-defining event name
	Activity string // activity event name; "" means any event counts
	Period   string // timeq.PeriodDay or timeq.PeriodWeek
	Periods  int    // number of columns, including period 0
}

// Validate rejects malformed configurations early.
func (c Config) Validate() error {
	if c.Cohort == "" {
		return fmt.Errorf("cohort event name must not be empty")
	}
	if !timeq.ValidPeriod(c.Period) {
		return fmt.Errorf("period %q must be day or week", c.Period)
	}
	if c.Periods < 2 || c.Periods > MaxPeriods {
		return fmt.Errorf("periods must be 2-%d, got %d", MaxPeriods, c.Periods)
	}
	return nil
}

// Row is one cohort in the triangle.
type Row struct {
	Cohort   string    `json:"cohort"`   // bucket start, "2006-01-02"
	Size     int       `json:"size"`     // users whose first cohort event fell in this bucket
	Retained []int     `json:"retained"` // users active in period k, k = 0..Periods-1
	Percent  []float64 `json:"percent"`  // Retained[k] / Size * 100
}

// Result is a computed retention triangle.
type Result struct {
	Rows []Row `json:"rows"`
}

// Analyzer consumes an event stream and produces a Result. Feed order does
// not matter.
type Analyzer struct {
	cfg      Config
	firstSee map[string]time.Time      // user -> earliest cohort event time
	activity map[string]map[int64]bool // user -> set of active bucket starts (unix)
}

// New builds an analyzer for a validated config.
func New(cfg Config) (*Analyzer, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Analyzer{
		cfg:      cfg,
		firstSee: map[string]time.Time{},
		activity: map[string]map[int64]bool{},
	}, nil
}

// Feed offers one event.
func (a *Analyzer) Feed(e event.Event) {
	if e.Name == a.cfg.Cohort {
		if first, ok := a.firstSee[e.User]; !ok || e.Time.Before(first) {
			a.firstSee[e.User] = e.Time
		}
	}
	if a.cfg.Activity == "" || e.Name == a.cfg.Activity {
		b := timeq.Bucket(e.Time, a.cfg.Period).Unix()
		if a.activity[e.User] == nil {
			a.activity[e.User] = map[int64]bool{}
		}
		a.activity[e.User][b] = true
	}
}

// Finalize computes the triangle over everything fed so far. Rows are
// sorted by cohort bucket ascending.
func (a *Analyzer) Finalize() Result {
	type agg struct {
		size     int
		retained []int
	}
	cohorts := map[int64]*agg{}
	for user, first := range a.firstSee {
		bucket := timeq.Bucket(first, a.cfg.Period)
		key := bucket.Unix()
		c := cohorts[key]
		if c == nil {
			c = &agg{retained: make([]int, a.cfg.Periods)}
			cohorts[key] = c
		}
		c.size++
		active := a.activity[user]
		for k := 0; k < a.cfg.Periods; k++ {
			pb := timeq.AddPeriods(bucket, a.cfg.Period, k).Unix()
			if active[pb] {
				c.retained[k]++
			}
		}
	}

	keys := make([]int64, 0, len(cohorts))
	for k := range cohorts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	res := Result{}
	for _, k := range keys {
		c := cohorts[k]
		row := Row{
			Cohort:   timeq.FormatBucket(time.Unix(k, 0).UTC(), a.cfg.Period),
			Size:     c.size,
			Retained: c.retained,
			Percent:  make([]float64, a.cfg.Periods),
		}
		for i, n := range c.retained {
			row.Percent[i] = 100 * float64(n) / float64(c.size)
		}
		res.Rows = append(res.Rows, row)
	}
	return res
}
