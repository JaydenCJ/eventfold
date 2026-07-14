// Package cli wires flags, subcommands and exit codes for the eventfold
// binary. All command logic delegates to the pure packages (store, query,
// funnel, retention, rollup, render) so it stays unit-testable in-process.
package cli

import (
	"flag"
	"fmt"
	"io"

	"github.com/JaydenCJ/eventfold/internal/store"
	"github.com/JaydenCJ/eventfold/internal/timeq"
	"github.com/JaydenCJ/eventfold/internal/version"
)

// Exit codes. Scripts can rely on these.
const (
	ExitOK      = 0 // success
	ExitData    = 1 // ingest --strict rejected at least one event
	ExitUsage   = 2 // bad flags or arguments
	ExitRuntime = 3 // I/O or data-directory failure
)

// DefaultDir is where events land unless --dir says otherwise.
const DefaultDir = "./eventfold-data"

const usageText = `eventfold — product analytics in one binary

Usage:
  eventfold <command> [flags] [args]

Commands:
  ingest      validate and append NDJSON events into day partitions
  funnel      ordered multi-step conversion within a time window
  retention   cohort retention triangle (day or week periods)
  count       event counts and unique users per day/week bucket
  events      distinct event names with counts and unique users
  rollup      precompute per-day rollup files for fast daily counts
  serve       local JSON API on 127.0.0.1 (never a public interface)
  version     print the version

Every command takes --dir PATH (data directory, default "./eventfold-data").
Query commands take --format text|json, --since/--until YYYY-MM-DD.
Run "eventfold <command> -h" for the full flag list.

Exit codes: 0 ok · 1 strict ingest rejected events · 2 usage · 3 runtime.
`

// Run executes one CLI invocation and returns its exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return ExitUsage
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "eventfold %s\n", version.Version)
		return ExitOK
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usageText)
		return ExitOK
	case "ingest":
		return runIngest(rest, stdout, stderr)
	case "funnel":
		return runFunnel(rest, stdout, stderr)
	case "retention":
		return runRetention(rest, stdout, stderr)
	case "count":
		return runCount(rest, stdout, stderr)
	case "events":
		return runEvents(rest, stdout, stderr)
	case "rollup":
		return runRollup(rest, stdout, stderr)
	case "serve":
		return runServe(rest, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "eventfold: unknown command %q\n\n", cmd)
		fmt.Fprint(stderr, usageText)
		return ExitUsage
	}
}

// newFlagSet builds a silent FlagSet whose errors we render ourselves.
func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

// usageErr prints a message and returns the usage exit code.
func usageErr(stderr io.Writer, format string, args ...any) int {
	fmt.Fprintf(stderr, "eventfold: "+format+"\n", args...)
	return ExitUsage
}

// runtimeErr prints a message and returns the runtime exit code.
func runtimeErr(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "eventfold: %v\n", err)
	return ExitRuntime
}

// openStore resolves --dir into a store handle.
func openStore(dir string) (*store.Store, error) {
	return store.Open(dir)
}

// parseRangeFlags converts --since/--until into a timeq.Range.
func parseRangeFlags(since, until string) (timeq.Range, error) {
	return timeq.ParseRange(since, until)
}

// checkFormat validates --format for query commands.
func checkFormat(format string) error {
	if format != "text" && format != "json" {
		return fmt.Errorf("--format must be text or json, got %q", format)
	}
	return nil
}
