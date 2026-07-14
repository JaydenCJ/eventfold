// Package server exposes the query engine as a local JSON API. It is a thin
// HTTP skin over internal/query — the CLI and the API share every line of
// analytics logic, so they can never disagree.
//
// The server is loopback-only by design: eventfold is a personal analytics
// engine, not a public collector. Anything that must be reachable from the
// network belongs behind a reverse proxy the operator controls.
package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/JaydenCJ/eventfold/internal/funnel"
	"github.com/JaydenCJ/eventfold/internal/ingestio"
	"github.com/JaydenCJ/eventfold/internal/query"
	"github.com/JaydenCJ/eventfold/internal/retention"
	"github.com/JaydenCJ/eventfold/internal/store"
	"github.com/JaydenCJ/eventfold/internal/timeq"
	"github.com/JaydenCJ/eventfold/internal/version"
)

// MaxIngestBody caps one POST /v1/ingest request (16 MiB of NDJSON).
const MaxIngestBody = 16 << 20

// Server handles the /v1 API against one store.
type Server struct {
	st  *store.Store
	mux *http.ServeMux
}

// New builds the handler.
func New(st *store.Store) *Server {
	s := &Server{st: st, mux: http.NewServeMux()}
	s.mux.HandleFunc("/v1/health", s.handleHealth)
	s.mux.HandleFunc("/v1/ingest", s.handleIngest)
	s.mux.HandleFunc("/v1/events", s.handleEvents)
	s.mux.HandleFunc("/v1/count", s.handleCount)
	s.mux.HandleFunc("/v1/funnel", s.handleFunnel)
	s.mux.HandleFunc("/v1/retention", s.handleRetention)
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "use GET")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": version.Version})
}

func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "use POST with an NDJSON body")
		return
	}
	body := http.MaxBytesReader(w, r.Body, MaxIngestBody)
	stats, invalid, err := ingestio.Stream(s.st, body, nil)
	if err != nil {
		if strings.Contains(err.Error(), "request body too large") {
			writeErr(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("body exceeds %d bytes", MaxIngestBody))
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"written":    stats.Written,
		"duplicates": stats.Duplicates,
		"invalid":    invalid,
	})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "use GET")
		return
	}
	rng, ok := rangeParam(w, r)
	if !ok {
		return
	}
	aggs, _, err := query.Events(s.st, rng)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeResult(w, "events", aggs)
}

func (s *Server) handleCount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "use GET")
		return
	}
	name := r.URL.Query().Get("event")
	if name == "" {
		writeErr(w, http.StatusBadRequest, "missing ?event=")
		return
	}
	by := r.URL.Query().Get("by")
	if by == "" {
		by = timeq.PeriodDay
	}
	if !timeq.ValidPeriod(by) {
		writeErr(w, http.StatusBadRequest, "by must be day or week")
		return
	}
	rng, ok := rangeParam(w, r)
	if !ok {
		return
	}
	buckets, err := query.Count(s.st, name, by, rng)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeResult(w, "count", buckets)
}

func (s *Server) handleFunnel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "use GET")
		return
	}
	q := r.URL.Query()
	windowStr := q.Get("window")
	if windowStr == "" {
		windowStr = "7d"
	}
	window, err := timeq.ParseWindow(windowStr)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	cfg := funnel.Config{Steps: splitSteps(q.Get("steps")), Window: window, By: q.Get("by")}
	if q.Get("steps") == "" {
		writeErr(w, http.StatusBadRequest, "missing ?steps=a,b,c")
		return
	}
	if err := cfg.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rng, ok := rangeParam(w, r)
	if !ok {
		return
	}
	res, _, err := query.Funnel(s.st, cfg, rng)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeResult(w, "funnel", res)
}

func (s *Server) handleRetention(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "use GET")
		return
	}
	q := r.URL.Query()
	period := q.Get("period")
	if period == "" {
		period = timeq.PeriodWeek
	}
	periods := 8
	if p := q.Get("periods"); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "periods must be an integer")
			return
		}
		periods = n
	}
	cfg := retention.Config{
		Cohort:   q.Get("cohort"),
		Activity: q.Get("activity"),
		Period:   period,
		Periods:  periods,
	}
	if err := cfg.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rng, ok := rangeParam(w, r)
	if !ok {
		return
	}
	res, _, err := query.Retention(s.st, cfg, rng)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeResult(w, "retention", res)
}

// rangeParam parses ?since= / ?until=; on failure it writes a 400 and
// returns ok=false.
func rangeParam(w http.ResponseWriter, r *http.Request) (timeq.Range, bool) {
	rng, err := timeq.ParseRange(r.URL.Query().Get("since"), r.URL.Query().Get("until"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return timeq.Range{}, false
	}
	return rng, true
}

func splitSteps(s string) []string {
	parts := strings.Split(s, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func writeResult(w http.ResponseWriter, kind string, payload any) {
	writeJSON(w, http.StatusOK, map[string]any{
		"tool":           "eventfold",
		"schema_version": 1,
		"kind":           kind,
		"result":         payload,
	})
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(payload)
}
