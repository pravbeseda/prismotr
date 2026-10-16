// Command prismotr is the per-host hosting panel: it renders the host's site menu and
// read-only status cards from a configuration file ansible-hosts writes.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/pravbeseda/prismotr/internal/version"
)

// errVersionRequested is --version answered on stdout: a request, like -h, not a failure.
var errVersionRequested = errors.New("version requested")

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		// -h and --version have already printed their answer; both are requests.
		if errors.Is(err, flag.ErrHelp) || errors.Is(err, errVersionRequested) {
			return
		}
		fmt.Fprintf(os.Stderr, "prismotr: %v\n", err)
		os.Exit(1)
	}
}

// run parses the command line and answers it. Errors are printed by the caller, so the flag
// package stays quiet — except for -h, which is answered with the flag list on out.
func run(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("prismotr", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var showVersion bool
	flags.BoolVar(&showVersion, "version", false, "print the version and exit")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			flags.SetOutput(out)
			flags.Usage()
		}
		return err
	}
	if showVersion {
		if _, err := fmt.Fprintf(out, "prismotr %s\n", version.Current); err != nil {
			return fmt.Errorf("write to stdout: %w", err)
		}
		return errVersionRequested
	}
	return nil
}
