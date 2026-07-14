// Tests for the JSON API, driven entirely in-process through
// httptest.NewRecorder — no sockets, no network.
package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JaydenCJ/eventfold/internal/store"
)

const seedNDJSON = `{"event":"signup","user":"u1","ts":"2026-06-01T09:00:00Z","props":{"plan":"pro"}}
{"event":"activate","user":"u1","ts":"2026-06-01T09:20:00Z"}
{"event":"signup","user":"u2","ts":"2026-06-01T11:00:00Z"}
{"event":"activate","user":"u2","ts":"2026-06-05T11:00:00Z"}
{"event":"pageview","user":"u1","ts":"2026-06-08T12:00:00Z"}
`

// newServer builds a server over a seeded temp store.
func newServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(st)
	rec := post(srv, "/v1/ingest", seedNDJSON)
	if rec.Code != 200 {
		t.Fatalf("seed ingest: %d %s", rec.Code, rec.Body.String())
	}
	return srv
}

func get(srv *Server, url string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
	return rec
}

func post(srv *Server, url, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("POST", url, strings.NewReader(body)))
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, into any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
		t.Fatalf("bad JSON (%d): %v\n%s", rec.Code, err, rec.Body.String())
	}
}

func TestHealth(t *testing.T) {
	srv := newServer(t)
	rec := get(srv, "/v1/health")
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		OK      bool   `json:"ok"`
		Version string `json:"version"`
	}
	decode(t, rec, &body)
	if !body.OK || body.Version == "" {
		t.Fatalf("body = %+v", body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content type = %q", ct)
	}
}

func TestIngestReportsCounts(t *testing.T) {
	st, _ := store.Open(t.TempDir())
	srv := New(st)
	body := `{"event":"a","user":"u1","ts":"2026-06-01T10:00:00Z","id":"k1"}
garbage line
{"event":"a","user":"u1","ts":"2026-06-01T10:00:00Z","id":"k1"}
`
	rec := post(srv, "/v1/ingest", body)
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Written    int `json:"written"`
		Duplicates int `json:"duplicates"`
		Invalid    int `json:"invalid"`
	}
	decode(t, rec, &res)
	if res.Written != 1 || res.Duplicates != 1 || res.Invalid != 1 {
		t.Fatalf("res = %+v", res)
	}
}

func TestMethodAndPathRejections(t *testing.T) {
	{
		srv := newServer(t)
		if rec := get(srv, "/v1/ingest"); rec.Code != 405 {
			t.Fatalf("GET /v1/ingest = %d, want 405", rec.Code)
		}
	}
	{
		srv := newServer(t)
		for _, url := range []string{"/v1/events", "/v1/count?event=a", "/v1/funnel?steps=a,b", "/v1/retention?cohort=a"} {
			if rec := post(srv, url, ""); rec.Code != 405 {
				t.Errorf("POST %s = %d, want 405", url, rec.Code)
			}
		}
	}
	{
		srv := newServer(t)
		if rec := get(srv, "/v2/everything"); rec.Code != 404 {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	}
}

func TestFunnelEndpoint(t *testing.T) {
	{
		srv := newServer(t)
		rec := get(srv, "/v1/funnel?steps=signup,activate&window=2d")
		if rec.Code != 200 {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		var env struct {
			Tool   string `json:"tool"`
			Kind   string `json:"kind"`
			Result struct {
				Entered int `json:"entered"`
				Steps   []struct {
					Name  string `json:"name"`
					Users int    `json:"users"`
				} `json:"steps"`
			} `json:"result"`
		}
		decode(t, rec, &env)
		if env.Tool != "eventfold" || env.Kind != "funnel" {
			t.Fatalf("envelope = %+v", env)
		}
		// u2 activates 4 days after signup: outside the 2d window.
		if env.Result.Entered != 2 || env.Result.Steps[1].Users != 1 {
			t.Fatalf("result = %+v", env.Result)
		}
	}
	{
		srv := newServer(t)
		rec := get(srv, "/v1/funnel?steps=signup,activate")
		if rec.Code != 200 {
			t.Fatalf("status = %d", rec.Code)
		}
		var env struct {
			Result struct {
				Steps []struct {
					Users int `json:"users"`
				} `json:"steps"`
			} `json:"result"`
		}
		decode(t, rec, &env)
		// Default 7d window admits u2's day-4 activation.
		if env.Result.Steps[1].Users != 2 {
			t.Fatalf("steps = %+v", env.Result.Steps)
		}
	}
}

func TestBadQueryParamsReturn400(t *testing.T) {
	{
		srv := newServer(t)
		cases := []string{
			"/v1/funnel",                                             // missing steps
			"/v1/funnel?steps=one",                                   // too few steps
			"/v1/funnel?steps=a,b&window=banana",                     // bad window
			"/v1/funnel?steps=a,b&since=not-a-date",                  // bad since
			"/v1/funnel?steps=a,b&since=2026-07-01&until=2026-06-01", // inverted
		}
		for _, url := range cases {
			rec := get(srv, url)
			if rec.Code != 400 {
				t.Errorf("%s = %d, want 400", url, rec.Code)
				continue
			}
			var body struct {
				Error string `json:"error"`
			}
			decode(t, rec, &body)
			if body.Error == "" {
				t.Errorf("%s: error message empty", url)
			}
		}
	}
	{
		srv := newServer(t)
		for _, url := range []string{
			"/v1/retention", // missing cohort
			"/v1/retention?cohort=signup&period=month", // bad period
			"/v1/retention?cohort=signup&periods=one",  // non-integer
			"/v1/retention?cohort=signup&periods=999",  // too wide
		} {
			if rec := get(srv, url); rec.Code != 400 {
				t.Errorf("%s = %d, want 400", url, rec.Code)
			}
		}
	}
	{
		srv := newServer(t)
		for _, url := range []string{"/v1/count", "/v1/count?event=a&by=month"} {
			if rec := get(srv, url); rec.Code != 400 {
				t.Errorf("%s = %d, want 400", url, rec.Code)
			}
		}
	}
}

func TestRetentionEndpoint(t *testing.T) {
	srv := newServer(t)
	rec := get(srv, "/v1/retention?cohort=signup&period=week&periods=2")
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Result struct {
			Rows []struct {
				Cohort   string `json:"cohort"`
				Size     int    `json:"size"`
				Retained []int  `json:"retained"`
			} `json:"rows"`
		} `json:"result"`
	}
	decode(t, rec, &env)
	if len(env.Result.Rows) != 1 || env.Result.Rows[0].Size != 2 {
		t.Fatalf("rows = %+v", env.Result.Rows)
	}
	// u1's week-1 pageview counts as any-event activity.
	if env.Result.Rows[0].Retained[1] != 1 {
		t.Fatalf("retained = %+v", env.Result.Rows[0].Retained)
	}
}

func TestCountEndpoint(t *testing.T) {
	srv := newServer(t)
	rec := get(srv, "/v1/count?event=signup&by=day")
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var env struct {
		Result []struct {
			Start string `json:"start"`
			Count int    `json:"count"`
			Users int    `json:"users"`
		} `json:"result"`
	}
	decode(t, rec, &env)
	if len(env.Result) == 0 || env.Result[0].Start != "2026-06-01" || env.Result[0].Count != 2 {
		t.Fatalf("result = %+v", env.Result)
	}
}

func TestEventsEndpointWithRange(t *testing.T) {
	srv := newServer(t)
	rec := get(srv, "/v1/events?since=2026-06-08")
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var env struct {
		Result []struct {
			Name  string `json:"name"`
			Count int    `json:"count"`
		} `json:"result"`
	}
	decode(t, rec, &env)
	if len(env.Result) != 1 || env.Result[0].Name != "pageview" {
		t.Fatalf("result = %+v", env.Result)
	}
}

func TestIngestThenQueryRoundTrip(t *testing.T) {
	// The write path and read path must agree end-to-end over HTTP.
	st, _ := store.Open(t.TempDir())
	srv := New(st)
	post(srv, "/v1/ingest", `{"event":"ping","user":"u1","ts":"2026-06-01T00:00:00Z"}`+"\n")
	rec := get(srv, "/v1/count?event=ping")
	var env struct {
		Result []struct {
			Count int `json:"count"`
		} `json:"result"`
	}
	decode(t, rec, &env)
	if len(env.Result) != 1 || env.Result[0].Count != 1 {
		t.Fatalf("round trip lost the event: %+v", env.Result)
	}
}
