package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pravbeseda/prismotr/internal/version"
)

func noEnv(string) (string, bool) { return "", false }

func TestVersionFlagPrintsNameAndVersion(t *testing.T) {
	var out bytes.Buffer
	_, err := settings([]string{"--version"}, &out, noEnv)
	if !errors.Is(err, errVersionRequested) {
		t.Fatalf("settings(--version) = %v, want errVersionRequested", err)
	}
	if got, want := out.String(), "prismotr "+version.Current+"\n"; got != want {
		t.Errorf("printed %q, want %q", got, want)
	}
}

func TestHelpIsARequestNotAFailure(t *testing.T) {
	var out bytes.Buffer
	if _, err := settings([]string{"-h"}, &out, noEnv); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("settings(-h) = %v, want flag.ErrHelp", err)
	}
	if out.Len() == 0 {
		t.Error("-h printed nothing")
	}
}

func TestAnUnknownFlagIsAnError(t *testing.T) {
	if _, err := settings([]string{"--no-such-flag"}, io.Discard, noEnv); err == nil {
		t.Fatal("settings(--no-such-flag) = nil, want an error")
	}
}

// --listen beats PRISMOTR_LISTEN, which beats the default; --config has the deployment path
// as its default.
func TestListenPrecedenceAndDefaults(t *testing.T) {
	env := func(key string) (string, bool) {
		if key == "PRISMOTR_LISTEN" {
			return "127.0.0.1:8200", true
		}
		return "", false
	}
	cases := []struct {
		name   string
		args   []string
		env    func(string) (string, bool)
		listen string
	}{
		{"default", nil, noEnv, "127.0.0.1:8100"},
		{"env", nil, env, "127.0.0.1:8200"},
		{"flag over env", []string{"--listen", "127.0.0.1:8300"}, env, "127.0.0.1:8300"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opts, err := settings(c.args, io.Discard, c.env)
			if err != nil {
				t.Fatalf("settings: %v", err)
			}
			if opts.listen != c.listen {
				t.Errorf("listen = %q, want %q", opts.listen, c.listen)
			}
			if opts.config != "/etc/prismotr/prismotr.yaml" {
				t.Errorf("config = %q, want the deployment path", opts.config)
			}
		})
	}
}

// nginx holds the perimeter, so an address that faces the network is refused.
func TestAListenAddressOffLoopbackIsRefused(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:8100", "192.0.2.1:8100", "8100"} {
		if _, err := settings([]string{"--listen", addr}, io.Discard, noEnv); err == nil {
			t.Errorf("settings(--listen %s) = nil, want a refusal", addr)
		}
	}
	if _, err := settings([]string{"--listen", "localhost:8100"}, io.Discard, noEnv); err != nil {
		t.Errorf("settings(--listen localhost:8100) = %v, want accepted", err)
	}
}

func TestAMissingConfigurationIsAStartupError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prismotr.yaml")
	err := run(context.Background(), []string{"--config", path, "--listen", "127.0.0.1:0"}, io.Discard, noEnv)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("run = %v, want an error naming %s", err, path)
	}
}

// The daemon serves the page on the address it announces, reloads on SIGHUP and stops when
// its context is cancelled.
func TestServesReloadsAndStops(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prismotr.yaml")
	if err := os.WriteFile(path, []byte("host: host-a\nbase_path: /prismotr\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"--config", path, "--listen", "127.0.0.1:0"}, pw, noEnv)
	}()
	line := make([]byte, 256)
	n, err := pr.Read(line)
	if err != nil {
		t.Fatal(err)
	}
	announced := regexp.MustCompile(`serving on (\S+) under /prismotr`).FindStringSubmatch(string(line[:n]))
	if announced == nil {
		t.Fatalf("no address announced in %q", line[:n])
	}
	url := "http://" + announced[1] + "/prismotr"

	if body := fetch(t, url); !strings.Contains(body, "host-a - Admin Panel") {
		t.Errorf("page:\n%s", body)
	}

	if err := os.WriteFile(path, []byte("host: host-a\nbase_path: /prismotr\nsites: [{name: munin, kind: location, path: /munin}]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(fetch(t, url), "/munin") {
		if time.Now().After(deadline) {
			t.Fatal("munin not on the page 5s after SIGHUP")
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run returned %v after cancel, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not stop within 5s of cancel")
	}
}

func fetch(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	if err := errors.Join(err, resp.Body.Close()); err != nil {
		t.Fatal(err)
	}
	return string(body)
}
