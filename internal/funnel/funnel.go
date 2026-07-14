// Package funnel computes ordered multi-step conversion.
//
// A user converts through steps S0 → S1 → … → Sn when the events occur in
// that order (equal timestamps allowed, ties broken by ingestion order) and
// every step lands within the window measured from the anchoring S0 event.
// Every S0 occurrence is tried as an anchor, so a user who stalls after an
// early S0 but later completes the whole funnel still counts — the naive
// "first occurrence only" shortcut undercounts exactly the users a growth
// team cares about.
package funnel

import (
	"fmt"
	"sort"
	"time"

	"github.com/JaydenCJ/eventfold/internal/event"
)

// MaxSteps bounds funnel depth; deeper funnels are almost always a query bug.
const MaxSteps = 12

// Config describes a funnel query.
type Config struct {
	Steps  []string      // ordered step event names (≥ 2), repeats allowed
	Window time.Duration // max time from the anchoring first step
	By     string        // optional property key on the first-step event to segment by
}

// Validate rejects malformed configurations early, with CLI-quality messages.
func (c Config) Validate() error {
	if len(c.Steps) < 2 {
		return fmt.Errorf("a funnel needs at least 2 steps, got %d", len(c.Steps))
	}
	if len(c.Steps) > MaxSteps {
		return fmt.Errorf("a funnel supports at most %d steps, got %d", MaxSteps, len(c.Steps))
	}
	for i, s := range c.Steps {
		if s == "" {
			return fmt.Errorf("step %d is empty", i+1)
		}
	}
	if c.Window <= 0 {
		return fmt.Errorf("window must be positive")
	}
	return nil
}

// Step is one row of a funnel result.
type Step struct {
	Name        string  `json:"name"`
	Users       int     `json:"users"`
	PctOfFirst  float64 `json:"pct_of_first"`       // conversion from step 1
	PctOfPrev   float64 `json:"pct_of_previous"`    // conversion from the prior step
	MedianSecs  float64 `json:"median_seconds"`     // median time from step 1, 0 for step 1
	MedianCount int     `json:"median_sample_size"` // conversions the median is computed over
}

// Result is a computed funnel.
type Result struct {
	Steps    []Step    `json:"steps"`
	Entered  int       `json:"entered"` // users who performed step 1 at least once
	Segments []Segment `json:"segments,omitempty"`
}

// Segment is a funnel restricted to users whose anchoring first-step event
// carried one value of the --by property.
type Segment struct {
	Key     string `json:"key"`
	Entered int    `json:"entered"`
	Steps   []Step `json:"steps"`
}

// hit is one relevant event occurrence for one user.
type hit struct {
	ts    time.Time
	seq   int    // ingestion order, breaks timestamp ties deterministically
	steps []int  // step indexes this event name maps to (repeats allowed)
	seg   string // segment key, captured when the name matches step 0
}

// Analyzer consumes an event stream and produces a Result. Feed order does
// not matter; hits are re-sorted per user at Finalize time.
type Analyzer struct {
	cfg     Config
	nameIdx map[string][]int
	users   map[string][]hit
	seq     int
}

// New builds an analyzer for a validated config.
func New(cfg Config) (*Analyzer, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	idx := map[string][]int{}
	for i, name := range cfg.Steps {
		idx[name] = append(idx[name], i)
	}
	return &Analyzer{cfg: cfg, nameIdx: idx, users: map[string][]hit{}}, nil
}

// Feed offers one event; events whose name is not a funnel step are ignored.
func (a *Analyzer) Feed(e event.Event) {
	steps, ok := a.nameIdx[e.Name]
	if !ok {
		return
	}
	h := hit{ts: e.Time, seq: a.seq, steps: steps}
	a.seq++
	if a.cfg.By != "" && contains(steps, 0) {
		h.seg = e.Prop(a.cfg.By)
	}
	a.users[e.User] = append(a.users[e.User], h)
}

// Finalize computes the funnel over everything fed so far.
func (a *Analyzer) Finalize() Result {
	overall := newAccumulator(len(a.cfg.Steps))
	segments := map[string]*accumulator{}

	userIDs := make([]string, 0, len(a.users))
	for u := range a.users {
		userIDs = append(userIDs, u)
	}
	sort.Strings(userIDs) // deterministic iteration

	for _, u := range userIDs {
		hits := a.users[u]
		sort.SliceStable(hits, func(i, j int) bool {
			if hits[i].ts.Equal(hits[j].ts) {
				return hits[i].seq < hits[j].seq
			}
			return hits[i].ts.Before(hits[j].ts)
		})
		depth, durations, seg := a.bestPath(hits)
		if depth == 0 {
			continue
		}
		overall.add(depth, durations)
		if a.cfg.By != "" {
			acc := segments[seg]
			if acc == nil {
				acc = newAccumulator(len(a.cfg.Steps))
				segments[seg] = acc
			}
			acc.add(depth, durations)
		}
	}

	res := Result{Entered: overall.reached[0], Steps: overall.steps(a.cfg.Steps)}
	if a.cfg.By != "" {
		keys := make([]string, 0, len(segments))
		for k := range segments {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if segments[keys[i]].reached[0] != segments[keys[j]].reached[0] {
				return segments[keys[i]].reached[0] > segments[keys[j]].reached[0]
			}
			return keys[i] < keys[j]
		})
		for _, k := range keys {
			acc := segments[k]
			res.Segments = append(res.Segments, Segment{
				Key:     k,
				Entered: acc.reached[0],
				Steps:   acc.steps(a.cfg.Steps),
			})
		}
	}
	return res
}

// bestPath tries every step-0 occurrence as an anchor and returns the
// deepest conversion; ties prefer the anchor that completes that depth
// earliest. durations[i] is the time from anchor to step i.
func (a *Analyzer) bestPath(hits []hit) (depth int, durations []time.Duration, seg string) {
	var bestEnd time.Time
	for anchorIdx, anchor := range hits {
		if !contains(anchor.steps, 0) {
			continue
		}
		d, durs, end := a.walk(hits, anchorIdx)
		if d > depth || (d == depth && d > 0 && end.Before(bestEnd)) {
			depth, durations, bestEnd, seg = d, durs, end, anchor.seg
		}
	}
	return depth, durations, seg
}

// walk greedily matches steps 1..n after the anchor, inside the window.
func (a *Analyzer) walk(hits []hit, anchorIdx int) (int, []time.Duration, time.Time) {
	anchor := hits[anchorIdx]
	deadline := anchor.ts.Add(a.cfg.Window)
	durations := make([]time.Duration, 1, len(a.cfg.Steps))
	next := 1
	end := anchor.ts
	for _, h := range hits[anchorIdx+1:] {
		if next >= len(a.cfg.Steps) {
			break
		}
		if h.ts.After(deadline) {
			break
		}
		if contains(h.steps, next) {
			durations = append(durations, h.ts.Sub(anchor.ts))
			end = h.ts
			next++
		}
	}
	return next, durations, end
}

// accumulator gathers per-step reach counts and time-to-step samples.
type accumulator struct {
	reached   []int
	durations [][]time.Duration
}

func newAccumulator(n int) *accumulator {
	return &accumulator{reached: make([]int, n), durations: make([][]time.Duration, n)}
}

func (acc *accumulator) add(depth int, durations []time.Duration) {
	for i := 0; i < depth; i++ {
		acc.reached[i]++
		acc.durations[i] = append(acc.durations[i], durations[i])
	}
}

func (acc *accumulator) steps(names []string) []Step {
	out := make([]Step, len(names))
	first := acc.reached[0]
	for i, name := range names {
		st := Step{Name: name, Users: acc.reached[i]}
		if first > 0 {
			st.PctOfFirst = 100 * float64(acc.reached[i]) / float64(first)
		}
		if i == 0 {
			st.PctOfFirst = pctOrZero(first)
			st.PctOfPrev = pctOrZero(first)
		} else if acc.reached[i-1] > 0 {
			st.PctOfPrev = 100 * float64(acc.reached[i]) / float64(acc.reached[i-1])
		}
		if i > 0 && len(acc.durations[i]) > 0 {
			st.MedianSecs = median(acc.durations[i]).Seconds()
			st.MedianCount = len(acc.durations[i])
		}
		out[i] = st
	}
	return out
}

func pctOrZero(n int) float64 {
	if n > 0 {
		return 100
	}
	return 0
}

// median returns the middle sample (lower of the two middles for even
// counts, so the value is always one a real user produced).
func median(ds []time.Duration) time.Duration {
	sorted := append([]time.Duration(nil), ds...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[(len(sorted)-1)/2]
}

func contains(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
