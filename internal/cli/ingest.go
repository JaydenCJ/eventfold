package cli

import (
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/JaydenCJ/eventfold/internal/ingestio"
	"github.com/JaydenCJ/eventfold/internal/store"
)

// runIngest reads NDJSON events from files (or stdin as "-"), validates each
// line, and appends them into day partitions.
func runIngest(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("ingest", stderr)
	dir := fs.String("dir", DefaultDir, "data directory")
	strict := fs.Bool("strict", false, "fail (exit 1) when any line is invalid")
	quiet := fs.Bool("quiet", false, "suppress per-line error output")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: eventfold ingest [--dir PATH] [--strict] [--quiet] [file ...|-]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	st, err := openStore(*dir)
	if err != nil {
		return runtimeErr(stderr, err)
	}

	inputs := fs.Args()
	if len(inputs) == 0 {
		inputs = []string{"-"}
	}

	total := store.AppendStats{Days: map[string]int{}}
	invalid := 0
	for _, path := range inputs {
		var r io.Reader
		var f *os.File
		name := path
		if path == "-" {
			r = os.Stdin
			name = "stdin"
		} else {
			var err error
			f, err = os.Open(path)
			if err != nil {
				return runtimeErr(stderr, err)
			}
			r = f
		}
		onErr := func(line int, err error) {
			if !*quiet {
				fmt.Fprintf(stderr, "eventfold: %s:%d: %v\n", name, line, err)
			}
		}
		stats, bad, err := ingestio.Stream(st, r, onErr)
		if f != nil {
			f.Close() // close each file as soon as it is drained, not at return
		}
		if err != nil {
			return runtimeErr(stderr, fmt.Errorf("%s: %v", name, err))
		}
		invalid += bad
		total.Written += stats.Written
		total.Duplicates += stats.Duplicates
		for d, n := range stats.Days {
			total.Days[d] += n
		}
	}

	days := make([]string, 0, len(total.Days))
	for d := range total.Days {
		days = append(days, d)
	}
	sort.Strings(days)
	fmt.Fprintf(stdout, "ingested %d event%s into %d day file%s (%d duplicate, %d invalid)\n",
		total.Written, plural(total.Written), len(days), plural(len(days)), total.Duplicates, invalid)
	for _, d := range days {
		fmt.Fprintf(stdout, "  %s  +%d\n", d, total.Days[d])
	}
	if *strict && invalid > 0 {
		fmt.Fprintf(stderr, "eventfold: --strict: %d invalid line%s rejected\n", invalid, plural(invalid))
		return ExitData
	}
	return ExitOK
}

// plural returns "s" when n != 1, so summaries never read "1 events".
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
