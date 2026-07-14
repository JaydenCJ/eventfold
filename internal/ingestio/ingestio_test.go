// Tests for the shared NDJSON ingest streamer: valid/invalid mixing, blank
// lines, error callbacks, batching, and dedup pass-through.
package ingestio

import (
	"strings"
	"testing"

	"github.com/JaydenCJ/eventfold/internal/event"
	"github.com/JaydenCJ/eventfold/internal/store"
)

func openTemp(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestStreamValidBlankAndEmptyInput(t *testing.T) {
	{
		s := openTemp(t)
		in := `{"event":"a","user":"u1","ts":"2026-06-01T10:00:00Z"}
{"event":"b","user":"u2","ts":"2026-06-02T10:00:00Z"}
`
		stats, invalid, err := Stream(s, strings.NewReader(in), nil)
		if err != nil {
			t.Fatal(err)
		}
		if stats.Written != 2 || invalid != 0 {
			t.Fatalf("stats = %+v, invalid = %d", stats, invalid)
		}
		if stats.Days["2026-06-01"] != 1 || stats.Days["2026-06-02"] != 1 {
			t.Fatalf("days = %v", stats.Days)
		}
	}
	{
		s := openTemp(t)
		in := "\n\n{\"event\":\"a\",\"user\":\"u\",\"ts\":\"2026-06-01T00:00:00Z\"}\n   \n"
		stats, invalid, err := Stream(s, strings.NewReader(in), nil)
		if err != nil {
			t.Fatal(err)
		}
		if stats.Written != 1 || invalid != 0 {
			t.Fatalf("blank lines mishandled: written=%d invalid=%d", stats.Written, invalid)
		}
	}
	{
		s := openTemp(t)
		stats, invalid, err := Stream(s, strings.NewReader(""), nil)
		if err != nil {
			t.Fatal(err)
		}
		if stats.Written != 0 || invalid != 0 {
			t.Fatalf("empty input: %+v / %d", stats, invalid)
		}
	}
}

func TestStreamCountsInvalidWithLineNumbers(t *testing.T) {
	s := openTemp(t)
	in := `{"event":"a","user":"u1","ts":"2026-06-01T10:00:00Z"}
garbage
{"event":"b","ts":"2026-06-01T10:00:00Z"}
{"event":"c","user":"u1","ts":"2026-06-01T10:00:00Z"}
`
	var badLines []int
	stats, invalid, err := Stream(s, strings.NewReader(in), func(line int, err error) {
		badLines = append(badLines, line)
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Written != 2 || invalid != 2 {
		t.Fatalf("written = %d, invalid = %d", stats.Written, invalid)
	}
	if len(badLines) != 2 || badLines[0] != 2 || badLines[1] != 3 {
		t.Fatalf("bad line numbers = %v", badLines)
	}
}

func TestStreamDeduplicatesById(t *testing.T) {
	s := openTemp(t)
	line := `{"event":"a","user":"u","ts":"2026-06-01T00:00:00Z","id":"k1"}`
	if _, _, err := Stream(s, strings.NewReader(line+"\n"), nil); err != nil {
		t.Fatal(err)
	}
	stats, _, err := Stream(s, strings.NewReader(line+"\n"+line+"\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Written != 0 || stats.Duplicates != 2 {
		t.Fatalf("dedup stats = %+v", stats)
	}
}

func TestStreamFlushesAcrossBatches(t *testing.T) {
	// More lines than one batch: everything must still land.
	s := openTemp(t)
	var sb strings.Builder
	n := BatchSize + 25
	for i := 0; i < n; i++ {
		sb.WriteString(`{"event":"a","user":"u`)
		sb.WriteString(strings.Repeat("x", i%3)) // a few distinct users
		sb.WriteString(`","ts":"2026-06-01T00:00:00Z"}` + "\n")
	}
	stats, _, err := Stream(s, strings.NewReader(sb.String()), nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Written != n {
		t.Fatalf("written = %d, want %d", stats.Written, n)
	}
	var count int
	if _, err := s.Scan(store.ScanQuery{}, func(event.Event) error { count++; return nil }); err != nil {
		t.Fatal(err)
	}
	if count != n {
		t.Fatalf("stored = %d, want %d", count, n)
	}
}
