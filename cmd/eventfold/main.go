// Command eventfold is product analytics in one binary: it ingests events
// into day-partitioned NDJSON files and answers funnel, retention and count
// queries from the CLI or a local JSON API.
package main

import (
	"os"

	"github.com/JaydenCJ/eventfold/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
