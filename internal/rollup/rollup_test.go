// Tests for rollup files: correct per-day aggregates, byte-size freshness
// fingerprinting, and incremental rebuilds after appends.
package rollup

import (
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/JaydenCJ/eventfold/internal/event"
	"github.com/JaydenCJ/eventfold/internal/store"
)

func ev(name, user string, day, hour int) event.Event {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	return event.Event{
		Name: name,
		User: user,
		Time: base.AddDate(0, 0, day).Add(time.Duration(hour) * time.Hour),
	}
}

func seed(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Append([]event.Event{
		ev("signup", "u1", 0, 9),
		ev("signup", "u2", 0, 10),
		ev("pageview", "u1", 0, 11),
		ev("pageview", "u1", 0, 12), // same user twice: users stays 1
		ev("signup", "u3", 1, 9),
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestBuildDayAggregates(t *testing.T) {
	s := seed(t)
	d, err := BuildDay(s, "2026-06-01")
	if err != nil {
		t.Fatalf("BuildDay: %v", err)
	}
	if d.Events["signup"].Count != 2 || d.Events["signup"].Users != 2 {
		t.Fatalf("signup agg = %+v", d.Events["signup"])
	}
	if d.Events["pageview"].Count != 2 || d.Events["pageview"].Users != 1 {
		t.Fatalf("pageview agg = %+v", d.Events["pageview"])
	}
	if d.SchemaVersion != SchemaVersion || d.Date != "2026-06-01" {
		t.Fatalf("metadata = %+v", d)
	}
}

func TestFreshnessLifecycle(t *testing.T) {
	{
		s := seed(t)
		d, fresh, err := Load(s, "2026-06-01")
		if err != nil || d != nil || fresh {
			t.Fatalf("missing rollup: d=%v fresh=%v err=%v", d, fresh, err)
		}
	}
	{
		s := seed(t)
		if _, err := BuildDay(s, "2026-06-01"); err != nil {
			t.Fatal(err)
		}
		if _, fresh, err := Load(s, "2026-06-01"); err != nil || !fresh {
			t.Fatalf("just-built rollup should be fresh (err=%v)", err)
		}
		// Any append grows the partition, so the fingerprint must break.
		if _, err := s.Append([]event.Event{ev("signup", "u4", 0, 13)}); err != nil {
			t.Fatal(err)
		}
		if _, fresh, _ := Load(s, "2026-06-01"); fresh {
			t.Fatalf("rollup should be stale after append")
		}
	}
}

func TestBuildSkipsFreshAndRebuildsStale(t *testing.T) {
	s := seed(t)
	first, err := Build(s, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Built) != 2 || len(first.Fresh) != 0 {
		t.Fatalf("first build = %+v", first)
	}
	second, err := Build(s, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Built) != 0 || len(second.Fresh) != 2 {
		t.Fatalf("second build = %+v", second)
	}
	// Stale one day only; the other must stay untouched.
	if _, err := s.Append([]event.Event{ev("signup", "u9", 1, 14)}); err != nil {
		t.Fatal(err)
	}
	third, err := Build(s, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(third.Built) != 1 || third.Built[0] != "2026-06-02" || len(third.Fresh) != 1 {
		t.Fatalf("third build = %+v", third)
	}
}

func TestBuildForceRebuildsEverything(t *testing.T) {
	s := seed(t)
	if _, err := Build(s, false); err != nil {
		t.Fatal(err)
	}
	stats, err := Build(s, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats.Built) != 2 || len(stats.Fresh) != 0 {
		t.Fatalf("force build = %+v", stats)
	}
}

func TestDefensiveLoading(t *testing.T) {
	{
		s := seed(t)
		if _, err := BuildDay(s, "2026-06-01"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(Path(s, "2026-06-01"), []byte("{broken"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Load(s, "2026-06-01"); err == nil {
			t.Fatalf("corrupt rollup should error")
		}
	}
	{
		s := seed(t)
		d, err := BuildDay(s, "2026-06-01")
		if err != nil {
			t.Fatal(err)
		}
		// Rewrite the file claiming a future schema but a correct fingerprint:
		// version, not size, must force the rebuild.
		future := []byte(`{"schema_version":99,"date":"2026-06-01","source_bytes":` +
			strconv.FormatInt(d.SourceBytes, 10) + `,"events":{}}`)
		if err := os.WriteFile(Path(s, "2026-06-01"), future, 0o644); err != nil {
			t.Fatal(err)
		}
		_, fresh, err := Load(s, "2026-06-01")
		if err != nil {
			t.Fatalf("future schema should load without error: %v", err)
		}
		if fresh {
			t.Fatalf("future schema must be treated as stale, forcing a rebuild")
		}
	}
}
