package cli

import (
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/JaydenCJ/eventfold/internal/server"
	"github.com/JaydenCJ/eventfold/internal/version"
)

// DefaultAddr is where the JSON API listens: loopback only, by design.
const DefaultAddr = "127.0.0.1:8991"

// runServe starts the local JSON API. It refuses to bind non-loopback
// addresses: eventfold is a personal analytics engine, not a public
// collector, and exposing it should be an explicit reverse-proxy decision.
func runServe(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("serve", stderr)
	dir := fs.String("dir", DefaultDir, "data directory")
	addr := fs.String("addr", DefaultAddr, "listen address (loopback only)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: eventfold serve [--dir PATH] [--addr 127.0.0.1:8991]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() > 0 {
		return usageErr(stderr, "serve takes no positional arguments")
	}
	if err := checkLoopback(*addr); err != nil {
		return usageErr(stderr, "%v", err)
	}
	st, err := openStore(*dir)
	if err != nil {
		return runtimeErr(stderr, err)
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return runtimeErr(stderr, err)
	}
	fmt.Fprintf(stdout, "eventfold %s serving http://%s (data: %s)\n",
		version.Version, ln.Addr(), st.Dir())
	if err := http.Serve(ln, server.New(st)); err != nil {
		return runtimeErr(stderr, err)
	}
	return ExitOK
}

// checkLoopback rejects listen addresses that are not loopback.
func checkLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("--addr %q must be host:port", addr)
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("--addr %q is not a loopback address; put a reverse proxy in front instead", addr)
	}
	return nil
}
