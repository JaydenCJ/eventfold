// In-process CLI integration tests: real files in temp dirs, real flag
// parsing, asserted exit codes and output. No subprocesses, no network.
package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JaydenCJ/eventfold/internal/version"
)

// run executes one CLI invocation and returns (exit, stdout, stderr).
func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(args, &out, &errb)
	return code, out.String(), errb.String()
}

// writeNDJSON drops an events file into a temp location.
func writeNDJSON(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// seedDir ingests a small three-user journey and returns the data dir.
func seedDir(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	data := filepath.Join(tmp, "data")
	events := writeNDJSON(t, tmp, "events.ndjson", `
{"event":"signup","user":"u1","ts":"2026-06-01T09:00:00Z","props":{"plan":"pro"}}
{"event":"activate","user":"u1","ts":"2026-06-01T09:20:00Z"}
{"event":"subscribe","user":"u1","ts":"2026-06-02T10:00:00Z"}
{"event":"signup","user":"u2","ts":"2026-06-01T11:00:00Z","props":{"plan":"free"}}
{"event":"activate","user":"u2","ts":"2026-06-03T09:00:00Z"}
{"event":"signup","user":"u3","ts":"2026-06-08T09:00:00Z","props":{"plan":"pro"}}
{"event":"pageview","user":"u1","ts":"2026-06-08T12:00:00Z"}
`)
	code, _, errb := run(t, "ingest", "--dir", data, events)
	if code != ExitOK {
		t.Fatalf("seed ingest failed (%d): %s", code, errb)
	}
	return data
}

func TestVersionHelpAndUsage(t *testing.T) {
	{
		for _, args := range [][]string{{"version"}, {"--version"}, {"-v"}} {
			code, out, _ := run(t, args...)
			if code != ExitOK || out != "eventfold "+version.Version+"\n" {
				t.Errorf("args %v: code=%d out=%q", args, code, out)
			}
		}
	}
	{
		code, _, errb := run(t)
		if code != ExitUsage || !strings.Contains(errb, "Usage:") {
			t.Fatalf("code=%d stderr=%q", code, errb)
		}
	}
	{
		code, _, errb := run(t, "frobnicate")
		if code != ExitUsage || !strings.Contains(errb, "unknown command") {
			t.Fatalf("code=%d stderr=%q", code, errb)
		}
	}
	{
		code, out, _ := run(t, "help")
		if code != ExitOK || !strings.Contains(out, "funnel") {
			t.Fatalf("code=%d out=%q", code, out)
		}
	}
}

func TestIngestReportsPerDaySplit(t *testing.T) {
	tmp := t.TempDir()
	data := filepath.Join(tmp, "data")
	f := writeNDJSON(t, tmp, "e.ndjson", `{"event":"a","user":"u1","ts":"2026-06-01T10:00:00Z"}
{"event":"a","user":"u2","ts":"2026-06-02T10:00:00Z"}
`)
	code, out, _ := run(t, "ingest", "--dir", data, f)
	if code != ExitOK {
		t.Fatalf("exit = %d", code)
	}
	for _, want := range []string{"ingested 2 events into 2 day files", "2026-06-01  +1", "2026-06-02  +1"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(data, "events", "2026-06-01.ndjson")); err != nil {
		t.Fatalf("partition missing: %v", err)
	}
}

func TestIngestInvalidLineReporting(t *testing.T) {
	{
		tmp := t.TempDir()
		data := filepath.Join(tmp, "data")
		f := writeNDJSON(t, tmp, "e.ndjson", `{"event":"a","user":"u1","ts":"2026-06-01T10:00:00Z"}
not json at all
`)
		code, out, errb := run(t, "ingest", "--dir", data, f)
		if code != ExitOK {
			t.Fatalf("exit = %d", code)
		}
		if !strings.Contains(out, "1 invalid") {
			t.Errorf("invalid count missing: %s", out)
		}
		if !strings.Contains(errb, "e.ndjson:2") {
			t.Errorf("per-line error missing file:line: %s", errb)
		}
	}
	{
		tmp := t.TempDir()
		data := filepath.Join(tmp, "data")
		f := writeNDJSON(t, tmp, "e.ndjson", "garbage\n")
		_, _, errb := run(t, "ingest", "--dir", data, "--quiet", f)
		if strings.Contains(errb, "e.ndjson:1") {
			t.Fatalf("quiet mode leaked per-line error: %s", errb)
		}
	}
}

func TestIngestFailureExitCodes(t *testing.T) {
	{
		tmp := t.TempDir()
		data := filepath.Join(tmp, "data")
		f := writeNDJSON(t, tmp, "e.ndjson", "garbage\n")
		code, _, errb := run(t, "ingest", "--dir", data, "--strict", f)
		if code != ExitData {
			t.Fatalf("exit = %d, want %d", code, ExitData)
		}
		if !strings.Contains(errb, "--strict") {
			t.Errorf("strict message missing: %s", errb)
		}
	}
	{
		code, _, _ := run(t, "ingest", "--dir", t.TempDir(), "/nonexistent/nope.ndjson")
		if code != ExitRuntime {
			t.Fatalf("exit = %d, want %d", code, ExitRuntime)
		}
	}
}

func TestIngestIsIdempotentWithIDs(t *testing.T) {
	tmp := t.TempDir()
	data := filepath.Join(tmp, "data")
	f := writeNDJSON(t, tmp, "e.ndjson",
		`{"event":"a","user":"u1","ts":"2026-06-01T10:00:00Z","id":"k1"}`+"\n")
	_, first, _ := run(t, "ingest", "--dir", data, f)
	// Singular counts must read as singular ("1 event", "1 day file").
	if !strings.Contains(first, "ingested 1 event into 1 day file (0 duplicate, 0 invalid)") {
		t.Fatalf("first ingest summary wrong: %q", first)
	}
	code, out, _ := run(t, "ingest", "--dir", data, f)
	if code != ExitOK || !strings.Contains(out, "ingested 0 events") || !strings.Contains(out, "1 duplicate") {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestFunnelTextOutput(t *testing.T) {
	{
		data := seedDir(t)
		code, out, _ := run(t, "funnel", "--dir", data,
			"--steps", "signup,activate,subscribe", "--window", "7d")
		if code != ExitOK {
			t.Fatalf("exit = %d", code)
		}
		for _, want := range []string{
			"funnel: signup → activate → subscribe",
			"entered: 3 users",
			"1. signup",
			"3. subscribe",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("funnel output missing %q:\n%s", want, out)
			}
		}
	}
	{
		data := seedDir(t)
		code, out, _ := run(t, "funnel", "--dir", data,
			"--steps", "signup,activate", "--window", "7d", "--by", "plan")
		if code != ExitOK {
			t.Fatalf("exit = %d", code)
		}
		if !strings.Contains(out, "plan = pro") || !strings.Contains(out, "plan = free") {
			t.Fatalf("segments missing:\n%s", out)
		}
	}
}

func TestFunnelJSONOutput(t *testing.T) {
	data := seedDir(t)
	code, out, _ := run(t, "funnel", "--dir", data,
		"--steps", "signup,activate", "--format", "json")
	if code != ExitOK {
		t.Fatalf("exit = %d", code)
	}
	var env struct {
		Tool   string `json:"tool"`
		Kind   string `json:"kind"`
		Result struct {
			Entered int `json:"entered"`
			Steps   []struct {
				Users int `json:"users"`
			} `json:"steps"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, out)
	}
	if env.Tool != "eventfold" || env.Kind != "funnel" || env.Result.Entered != 3 {
		t.Fatalf("envelope = %+v", env)
	}
	if env.Result.Steps[1].Users != 2 {
		t.Fatalf("steps = %+v", env.Result.Steps)
	}
}

func TestQueryUsageErrors(t *testing.T) {
	{
		data := seedDir(t)
		cases := [][]string{
			{"funnel", "--dir", data},                                            // no steps
			{"funnel", "--dir", data, "--steps", "only-one"},                     // too few
			{"funnel", "--dir", data, "--steps", "a,b", "--window", "yesterday"}, // bad window
			{"funnel", "--dir", data, "--steps", "a,b", "--format", "yaml"},      // bad format
			{"funnel", "--dir", data, "--steps", "a,b", "--since", "June 1st"},   // bad date
			{"funnel", "--dir", data, "--steps", "a,b", "--since", "2026-07-01", "--until", "2026-06-01"},
		}
		for _, args := range cases {
			if code, _, _ := run(t, args...); code != ExitUsage {
				t.Errorf("args %v: exit = %d, want %d", args, code, ExitUsage)
			}
		}
	}
	{
		data := seedDir(t)
		cases := [][]string{
			{"retention", "--dir", data}, // no cohort
			{"retention", "--dir", data, "--cohort", "signup", "--period", "month"}, // bad period
			{"retention", "--dir", data, "--cohort", "signup", "--periods", "1"},    // too few
			{"retention", "--dir", data, "--cohort", "signup", "--format", "csv"},   // bad format
		}
		for _, args := range cases {
			if code, _, _ := run(t, args...); code != ExitUsage {
				t.Errorf("args %v: exit = %d, want %d", args, code, ExitUsage)
			}
		}
	}
	{
		data := seedDir(t)
		cases := [][]string{
			{"count", "--dir", data},                                    // no event
			{"count", "--dir", data, "--event", "x", "--by", "month"},   // bad bucket
			{"count", "--dir", data, "--event", "x", "--format", "xml"}, // bad format
		}
		for _, args := range cases {
			if code, _, _ := run(t, args...); code != ExitUsage {
				t.Errorf("args %v: exit = %d, want %d", args, code, ExitUsage)
			}
		}
	}
}

func TestRetentionTextOutput(t *testing.T) {
	data := seedDir(t)
	code, out, _ := run(t, "retention", "--dir", data,
		"--cohort", "signup", "--period", "week", "--periods", "2")
	if code != ExitOK {
		t.Fatalf("exit = %d", code)
	}
	// Two weekly cohorts: week of 06-01 (u1, u2) and week of 06-08 (u3).
	if !strings.Contains(out, "2026-06-01") || !strings.Contains(out, "2026-06-08") {
		t.Fatalf("cohort rows missing:\n%s", out)
	}
	// u1 signed up week 0 and viewed a page in week 1: 50% p1 retention.
	if !strings.Contains(out, "50.0%") {
		t.Fatalf("expected 50%% p1 retention:\n%s", out)
	}
}

func TestRollupAndCountIntegration(t *testing.T) {
	{
		data := seedDir(t)
		code, out, _ := run(t, "rollup", "--dir", data)
		if code != ExitOK || !strings.Contains(out, "built") {
			t.Fatalf("rollup: code=%d out=%q", code, out)
		}
		code, out, _ = run(t, "count", "--dir", data, "--event", "signup", "--by", "day")
		if code != ExitOK {
			t.Fatalf("count exit = %d", code)
		}
		if !strings.Contains(out, "rollup") {
			t.Fatalf("count should be served from rollups:\n%s", out)
		}
		if !strings.Contains(out, "3 events across") {
			t.Fatalf("total wrong:\n%s", out)
		}
	}
	{
		data := seedDir(t)
		run(t, "rollup", "--dir", data)
		code, out, _ := run(t, "rollup", "--dir", data)
		if code != ExitOK || !strings.Contains(out, "already fresh") || strings.Contains(out, " built\n") {
			t.Fatalf("code=%d out=%q", code, out)
		}
	}
}

func TestEventsListing(t *testing.T) {
	{
		data := seedDir(t)
		code, out, _ := run(t, "events", "--dir", data)
		if code != ExitOK {
			t.Fatalf("exit = %d", code)
		}
		// signup (3) must be listed before activate (2).
		si, ai := strings.Index(out, "signup"), strings.Index(out, "activate")
		if si < 0 || ai < 0 || si > ai {
			t.Fatalf("event ordering wrong:\n%s", out)
		}
	}
	{
		data := seedDir(t)
		_, first, _ := run(t, "events", "--dir", data, "--format", "json")
		for i := 0; i < 3; i++ {
			_, next, _ := run(t, "events", "--dir", data, "--format", "json")
			if next != first {
				t.Fatalf("events JSON not byte-stable")
			}
		}
	}
}

func TestServeAddrValidation(t *testing.T) {
	{
		code, _, errb := run(t, "serve", "--dir", t.TempDir(), "--addr", "0.0.0.0:9999")
		if code != ExitUsage {
			t.Fatalf("exit = %d, want %d", code, ExitUsage)
		}
		if !strings.Contains(errb, "loopback") {
			t.Fatalf("stderr = %q", errb)
		}
	}
	{
		code, _, _ := run(t, "serve", "--dir", t.TempDir(), "--addr", "no-port-here")
		if code != ExitUsage {
			t.Fatalf("exit = %d, want %d", code, ExitUsage)
		}
	}
}

func TestQueriesOnEmptyStoreSucceed(t *testing.T) {
	// Read-only commands on a store that was never written to must not
	// create directories or fail.
	data := filepath.Join(t.TempDir(), "never-written")
	code, out, _ := run(t, "events", "--dir", data)
	if code != ExitOK || !strings.Contains(out, "no events in range") {
		t.Fatalf("code=%d out=%q", code, out)
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Fatalf("read-only command created the data dir")
	}
}
