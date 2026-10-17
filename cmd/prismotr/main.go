// Command prismotr is the per-host hosting panel: it renders the host's site menu and
// read-only status cards from a configuration file ansible-hosts writes.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pravbeseda/prismotr/internal/config"
	"github.com/pravbeseda/prismotr/internal/status"
	"github.com/pravbeseda/prismotr/internal/version"
	"github.com/pravbeseda/prismotr/internal/web"
)

const (
	defaultConfig = "/etc/prismotr/prismotr.yaml"
	defaultListen = "127.0.0.1:8100"
	// listenVariable is read from the environment file the service unit names.
	listenVariable = "PRISMOTR_LISTEN"

	readHeaderTimeout = 10 * time.Second
	shutdownTimeout   = 5 * time.Second
)

// errVersionRequested is --version answered on stdout: a request, like -h, not a failure.
var errVersionRequested = errors.New("version requested")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout, os.LookupEnv)
	stop()
	// -h and --version have already printed their answer; both are requests.
	if err == nil || errors.Is(err, flag.ErrHelp) || errors.Is(err, errVersionRequested) {
		return
	}
	fmt.Fprintf(os.Stderr, "prismotr: %v\n", err)
	os.Exit(1)
}

// options are the deployment settings; only the listen address has a product default,
// because binding to loopback says nothing about an installation.
type options struct {
	config string
	listen string
}

// run serves until ctx is cancelled. It announces the address it took on out, reloads the
// configuration on SIGHUP, and returns nil after a clean stop.
func run(ctx context.Context, args []string, out io.Writer, env func(string) (string, bool)) error {
	opts, err := settings(args, out, env)
	if err != nil {
		return err
	}
	log := slog.Default()
	cfg, err := config.Open(opts.config, log)
	if err != nil {
		return err
	}

	// The address is taken before it is announced: a journal that claims a port the daemon
	// never got would mislead whoever is hunting for the one it could not take.
	listener, err := net.Listen("tcp", opts.listen)
	if err != nil {
		return fmt.Errorf("serve on %s: %w", opts.listen, err)
	}
	if _, err := fmt.Fprintf(out, "prismotr %s serving on %s under %s\n",
		version.Current, listener.Addr(), cfg.Current().BasePath); err != nil {
		return errors.Join(fmt.Errorf("write to stdout: %w", err), listener.Close())
	}

	hangups := make(chan os.Signal, 1)
	signal.Notify(hangups, syscall.SIGHUP)
	defer func() {
		signal.Stop(hangups)
		close(hangups)
	}()
	go func() {
		for range hangups {
			// A failed reload is logged by the holder; the configuration in force stays.
			_ = cfg.Reload()
		}
	}()

	collect := func(ctx context.Context, cfg *config.Config, sites []config.Site) status.Page {
		return status.ReadPage(ctx, cfg, sites, status.Production, status.Options{})
	}
	server := &http.Server{
		Handler:           web.New(cfg.Current, version.Current, collect),
		ReadHeaderTimeout: readHeaderTimeout,
	}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()

	select {
	case err := <-served:
		return fmt.Errorf("serve on %s: %w", opts.listen, err)
	case <-ctx.Done():
		closing, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(closing); err != nil {
			return fmt.Errorf("stop serving: %w", err)
		}
		if err := <-served; !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve on %s: %w", opts.listen, err)
		}
		return nil
	}
}

// settings reads the command line and the environment. Errors are printed by the caller,
// so the flag package stays quiet — except for -h, which is answered with the flag list.
func settings(args []string, out io.Writer, env func(string) (string, bool)) (options, error) {
	flags := flag.NewFlagSet("prismotr", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var opts options
	var showVersion bool
	flags.BoolVar(&showVersion, "version", false, "print the version and exit")
	flags.StringVar(&opts.config, "config", defaultConfig, "the configuration file ansible-hosts writes")
	flags.StringVar(&opts.listen, "listen", "",
		"loopback address to serve on; defaults to "+listenVariable+", then "+defaultListen)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			flags.SetOutput(out)
			flags.Usage()
		}
		return options{}, err
	}
	if showVersion {
		if _, err := fmt.Fprintf(out, "prismotr %s\n", version.Current); err != nil {
			return options{}, fmt.Errorf("write to stdout: %w", err)
		}
		return options{}, errVersionRequested
	}
	if opts.listen == "" {
		if fromEnv, ok := env(listenVariable); ok && fromEnv != "" {
			opts.listen = fromEnv
		} else {
			opts.listen = defaultListen
		}
	}
	if err := requireLoopback(opts.listen); err != nil {
		return options{}, err
	}
	return opts, nil
}

// requireLoopback refuses an address that faces the network: nginx and its basic auth are
// the perimeter, and a daemon reachable on a public interface would serve the page to
// anyone who finds the port.
func requireLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("listen address %q: %w", addr, err)
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("listen address %q is not loopback: the panel is reached through nginx, "+
		"which is what authenticates it", addr)
}
