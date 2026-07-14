// Package ingestio streams NDJSON event sources into a store in bounded
// batches. It is shared by the CLI ingest command and the JSON API's
// POST /v1/ingest, so both accept exactly the same input.
package ingestio

import (
	"bufio"
	"io"
	"strings"

	"github.com/JaydenCJ/eventfold/internal/event"
	"github.com/JaydenCJ/eventfold/internal/store"
)

// BatchSize caps how many parsed events are held in memory before being
// flushed to disk, so arbitrarily large NDJSON files stream through.
const BatchSize = 10000

// Stream parses NDJSON lines from r and appends valid events to st. Invalid
// lines are counted and reported through onErr (which may be nil); they
// never abort the stream. The returned int is the invalid-line count.
func Stream(st *store.Store, r io.Reader, onErr func(line int, err error)) (store.AppendStats, int, error) {
	total := store.AppendStats{Days: map[string]int{}}
	invalid := 0
	batch := make([]event.Event, 0, BatchSize)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		stats, err := st.Append(batch)
		batch = batch[:0]
		if err != nil {
			return err
		}
		total.Written += stats.Written
		total.Duplicates += stats.Duplicates
		for d, n := range stats.Days {
			total.Days[d] += n
		}
		return nil
	}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), event.MaxLineSize+2)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		e, err := event.ParseLine(line)
		if err != nil {
			invalid++
			if onErr != nil {
				onErr(lineNo, err)
			}
			continue
		}
		batch = append(batch, e)
		if len(batch) == BatchSize {
			if err := flush(); err != nil {
				return total, invalid, err
			}
		}
	}
	if err := sc.Err(); err != nil {
		return total, invalid, err
	}
	return total, invalid, flush()
}
