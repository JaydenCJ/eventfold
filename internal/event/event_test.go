// Tests for the event record and its NDJSON codec: strict required fields,
// flexible timestamps, scalar property coercion, and a canonical stored form.
package event

import (
	"strings"
	"testing"
	"time"
)

func TestParseLineMinimalEvent(t *testing.T) {
	e, err := ParseLine([]byte(`{"event":"signup","user":"u1","ts":"2026-06-01T09:30:00Z"}`))
	if err != nil {
		t.Fatalf("ParseLine: %v", err)
	}
	if e.Name != "signup" || e.User != "u1" {
		t.Fatalf("got %+v", e)
	}
	want := time.Date(2026, 6, 1, 9, 30, 0, 0, time.UTC)
	if !e.Time.Equal(want) {
		t.Fatalf("time = %v, want %v", e.Time, want)
	}
}

func TestParseLineKeepsIDAndProps(t *testing.T) {
	e, err := ParseLine([]byte(`{"event":"pay","user":"u1","ts":"2026-06-01T00:00:00Z","id":"evt-1","props":{"plan":"pro"}}`))
	if err != nil {
		t.Fatalf("ParseLine: %v", err)
	}
	if e.ID != "evt-1" || e.Props["plan"] != "pro" {
		t.Fatalf("got %+v", e)
	}
}

func TestUTCNormalizationAndPropAccess(t *testing.T) {
	{
		// Offsets must land on the same UTC instant, and thus the same partition.
		e, err := ParseLine([]byte(`{"event":"a","user":"u","ts":"2026-06-01T08:30:00+09:00"}`))
		if err != nil {
			t.Fatalf("ParseLine: %v", err)
		}
		if got := e.Day(); got != "2026-05-31" {
			t.Fatalf("Day() = %q, want 2026-05-31 (23:30 UTC the previous day)", got)
		}
	}
	{
		loc := time.FixedZone("plus9", 9*3600)
		e := Event{Name: "a", User: "u", Time: time.Date(2026, 6, 1, 3, 0, 0, 0, loc)}
		if got := e.Day(); got != "2026-05-31" {
			t.Fatalf("Day() = %q, want 2026-05-31", got)
		}
	}
	{
		e := Event{Props: map[string]string{"plan": "pro"}}
		if e.Prop("plan") != "pro" {
			t.Fatalf("Prop(plan) = %q", e.Prop("plan"))
		}
		if e.Prop("missing") != "(none)" {
			t.Fatalf("Prop(missing) = %q, want (none)", e.Prop("missing"))
		}
	}
}

func TestParseLineUnixEpochs(t *testing.T) {
	{
		e, err := ParseLine([]byte(`{"event":"a","user":"u","ts":1780000000}`))
		if err != nil {
			t.Fatalf("ParseLine: %v", err)
		}
		if e.Time.Unix() != 1780000000 {
			t.Fatalf("unix = %d", e.Time.Unix())
		}
	}
	{
		// 13-digit epochs are interpreted as milliseconds.
		e, err := ParseLine([]byte(`{"event":"a","user":"u","ts":1780000000123}`))
		if err != nil {
			t.Fatalf("ParseLine: %v", err)
		}
		if e.Time.UnixMilli() != 1780000000123 {
			t.Fatalf("unixmilli = %d", e.Time.UnixMilli())
		}
	}
}

func TestParseLinePropCoercion(t *testing.T) {
	e, err := ParseLine([]byte(`{"event":"a","user":"u","ts":"2026-06-01T00:00:00Z",` +
		`"props":{"n":3,"f":2.5,"b":true,"s":"x","nil":null}}`))
	if err != nil {
		t.Fatalf("ParseLine: %v", err)
	}
	for k, want := range map[string]string{"n": "3", "f": "2.5", "b": "true", "s": "x"} {
		if e.Props[k] != want {
			t.Errorf("props[%q] = %q, want %q", k, e.Props[k], want)
		}
	}
	if _, ok := e.Props["nil"]; ok {
		t.Errorf("null property should be dropped")
	}
}

func TestParseLineRejections(t *testing.T) {
	{
		cases := map[string]string{
			"not json":             `{"event"`,
			"missing event":        `{"user":"u","ts":"2026-06-01T00:00:00Z"}`,
			"missing user":         `{"event":"a","ts":"2026-06-01T00:00:00Z"}`,
			"missing ts":           `{"event":"a","user":"u"}`,
			"bad ts string":        `{"event":"a","user":"u","ts":"yesterday"}`,
			"negative ts":          `{"event":"a","user":"u","ts":-5}`,
			"ts wrong type":        `{"event":"a","user":"u","ts":[1]}`,
			"nested prop":          `{"event":"a","user":"u","ts":"2026-06-01T00:00:00Z","props":{"x":{"y":1}}}`,
			"array prop":           `{"event":"a","user":"u","ts":"2026-06-01T00:00:00Z","props":{"x":[1]}}`,
			"unknown field":        `{"event":"a","user":"u","ts":"2026-06-01T00:00:00Z","extra":1}`,
			"control char in name": `{"event":"a\u0007b","user":"u","ts":"2026-06-01T00:00:00Z"}`,
			"year out of range":    `{"event":"a","user":"u","ts":"1969-01-01T00:00:00Z"}`,
		}
		for name, line := range cases {
			if _, err := ParseLine([]byte(line)); err == nil {
				t.Errorf("%s: expected error for %s", name, line)
			}
		}
	}
	{
		long := strings.Repeat("x", MaxNameLen+1)
		if _, err := ParseLine([]byte(`{"event":"` + long + `","user":"u","ts":"2026-06-01T00:00:00Z"}`)); err == nil {
			t.Errorf("over-long event name should be rejected")
		}
		longVal := strings.Repeat("v", MaxPropVal+1)
		if _, err := ParseLine([]byte(`{"event":"a","user":"u","ts":"2026-06-01T00:00:00Z","props":{"k":"` + longVal + `"}}`)); err == nil {
			t.Errorf("over-long property value should be rejected")
		}
	}
}

func TestMarshalLineRoundTrip(t *testing.T) {
	in := Event{
		Name:  "signup",
		User:  "u9",
		Time:  time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
		ID:    "k1",
		Props: map[string]string{"plan": "pro", "ref": "hn"},
	}
	line, err := in.MarshalLine()
	if err != nil {
		t.Fatalf("MarshalLine: %v", err)
	}
	out, err := ParseLine(line)
	if err != nil {
		t.Fatalf("ParseLine(round trip): %v", err)
	}
	if out.Name != in.Name || out.User != in.User || out.ID != in.ID ||
		!out.Time.Equal(in.Time) || out.Props["plan"] != "pro" || out.Props["ref"] != "hn" {
		t.Fatalf("round trip mismatch: %+v vs %+v", out, in)
	}
}

func TestMarshalLineIsDeterministic(t *testing.T) {
	e := Event{
		Name:  "a",
		User:  "u",
		Time:  time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		Props: map[string]string{"z": "1", "a": "2", "m": "3"},
	}
	first, err := e.MarshalLine()
	if err != nil {
		t.Fatalf("MarshalLine: %v", err)
	}
	for i := 0; i < 20; i++ {
		next, _ := e.MarshalLine()
		if string(next) != string(first) {
			t.Fatalf("non-deterministic marshal: %s vs %s", next, first)
		}
	}
}
