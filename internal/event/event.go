// Package event defines the eventfold event record and its NDJSON codec.
//
// One event is one JSON object on one line:
//
//	{"event":"signup","user":"u_419","ts":"2026-06-01T09:30:00Z","props":{"plan":"pro"}}
//
// Parsing is strict about the three required fields (event, user, ts) and
// lenient about property value types: scalars are coerced to strings so the
// rest of the engine never touches encoding/json again.
package event

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Limits keep single events bounded so one bad line can never blow up a scan.
const (
	MaxNameLen  = 128
	MaxUserLen  = 128
	MaxIDLen    = 128
	MaxProps    = 32
	MaxPropKey  = 64
	MaxPropVal  = 256
	MaxLineSize = 1 << 20 // 1 MiB per NDJSON line
)

// Event is one analytics event.
type Event struct {
	Name  string            // event name, e.g. "signup"
	User  string            // stable user or device identifier
	Time  time.Time         // event timestamp, always held in UTC
	ID    string            // optional idempotency key for deduplication
	Props map[string]string // optional flat string properties
}

// wire is the on-disk / on-the-wire JSON shape. The timestamp is kept raw so
// both RFC 3339 strings and unix numbers can be accepted.
type wire struct {
	Event string          `json:"event"`
	User  string          `json:"user"`
	TS    json.RawMessage `json:"ts"`
	ID    string          `json:"id,omitempty"`
	Props map[string]any  `json:"props,omitempty"`
}

// ParseLine decodes and validates a single NDJSON line.
func ParseLine(line []byte) (Event, error) {
	if len(line) > MaxLineSize {
		return Event{}, fmt.Errorf("line exceeds %d bytes", MaxLineSize)
	}
	var w wire
	dec := json.NewDecoder(strings.NewReader(string(line)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return Event{}, fmt.Errorf("invalid JSON: %v", err)
	}
	ts, err := parseTimestamp(w.TS)
	if err != nil {
		return Event{}, err
	}
	props, err := coerceProps(w.Props)
	if err != nil {
		return Event{}, err
	}
	e := Event{Name: w.Event, User: w.User, Time: ts, ID: w.ID, Props: props}
	if err := e.Validate(); err != nil {
		return Event{}, err
	}
	return e, nil
}

// Validate checks the invariants every stored event must satisfy.
func (e Event) Validate() error {
	switch {
	case e.Name == "":
		return errors.New(`missing "event" field`)
	case len(e.Name) > MaxNameLen:
		return fmt.Errorf("event name exceeds %d bytes", MaxNameLen)
	case hasControl(e.Name):
		return errors.New("event name contains control characters")
	case e.User == "":
		return errors.New(`missing "user" field`)
	case len(e.User) > MaxUserLen:
		return fmt.Errorf("user id exceeds %d bytes", MaxUserLen)
	case hasControl(e.User):
		return errors.New("user id contains control characters")
	case len(e.ID) > MaxIDLen:
		return fmt.Errorf("id exceeds %d bytes", MaxIDLen)
	case e.Time.IsZero():
		return errors.New(`missing "ts" field`)
	case e.Time.Year() < 1970 || e.Time.Year() > 3000:
		return fmt.Errorf("timestamp year %d out of range 1970-3000", e.Time.Year())
	case len(e.Props) > MaxProps:
		return fmt.Errorf("more than %d properties", MaxProps)
	}
	for k, v := range e.Props {
		if k == "" || len(k) > MaxPropKey {
			return fmt.Errorf("property key %q must be 1-%d bytes", k, MaxPropKey)
		}
		if len(v) > MaxPropVal {
			return fmt.Errorf("property %q value exceeds %d bytes", k, MaxPropVal)
		}
	}
	return nil
}

// MarshalLine renders the canonical stored form: UTC RFC 3339 timestamp,
// keys in a fixed order, properties sorted (encoding/json sorts map keys).
// The result carries no trailing newline.
func (e Event) MarshalLine() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	w := wire{
		Event: e.Name,
		User:  e.User,
		TS:    json.RawMessage(strconv.Quote(e.Time.UTC().Format(time.RFC3339Nano))),
		ID:    e.ID,
	}
	if len(e.Props) > 0 {
		w.Props = make(map[string]any, len(e.Props))
		for k, v := range e.Props {
			w.Props[k] = v
		}
	}
	return json.Marshal(w)
}

// Day returns the UTC day partition this event belongs to ("2006-01-02").
func (e Event) Day() string {
	return e.Time.UTC().Format("2006-01-02")
}

// Prop returns a property value, or "(none)" when absent — the placeholder
// segment key used by funnel breakdowns.
func (e Event) Prop(key string) string {
	if v, ok := e.Props[key]; ok {
		return v
	}
	return "(none)"
}

// parseTimestamp accepts RFC 3339 strings (with or without sub-seconds) and
// unix epoch numbers: seconds up to 1e11, milliseconds beyond that.
func parseTimestamp(raw json.RawMessage) (time.Time, error) {
	if len(raw) == 0 {
		return time.Time{}, errors.New(`missing "ts" field`)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		t, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return time.Time{}, fmt.Errorf("ts %q is not RFC 3339", s)
		}
		return t.UTC(), nil
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err == nil {
		if n < 0 {
			return time.Time{}, fmt.Errorf("ts %v is negative", n)
		}
		if n >= 1e11 { // 13-digit epochs are milliseconds
			return time.UnixMilli(int64(n)).UTC(), nil
		}
		return time.Unix(int64(n), 0).UTC(), nil
	}
	return time.Time{}, errors.New(`"ts" must be an RFC 3339 string or a unix epoch number`)
}

// coerceProps flattens scalar property values to strings and rejects nested
// structures, which have no meaning to the group-by engine.
func coerceProps(in map[string]any) (map[string]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(in))
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic error selection
	for _, k := range keys {
		switch v := in[k].(type) {
		case string:
			out[k] = v
		case float64:
			out[k] = strconv.FormatFloat(v, 'f', -1, 64)
		case bool:
			out[k] = strconv.FormatBool(v)
		case nil:
			// dropped: null carries no group-by value
		default:
			return nil, fmt.Errorf("property %q has non-scalar value", k)
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
