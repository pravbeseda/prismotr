package main

import (
	"bytes"
	"errors"
	"flag"
	"testing"

	"github.com/pravbeseda/prismotr/internal/version"
)

func TestVersionFlagPrintsNameAndVersion(t *testing.T) {
	var out bytes.Buffer
	err := run([]string{"--version"}, &out)
	if !errors.Is(err, errVersionRequested) {
		t.Fatalf("run(--version) = %v, want errVersionRequested", err)
	}
	if got, want := out.String(), "prismotr "+version.Current+"\n"; got != want {
		t.Errorf("printed %q, want %q", got, want)
	}
}

func TestHelpIsARequestNotAFailure(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"-h"}, &out); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("run(-h) = %v, want flag.ErrHelp", err)
	}
	if out.Len() == 0 {
		t.Error("-h printed nothing")
	}
}

func TestAnUnknownFlagIsAnError(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"--no-such-flag"}, &out); err == nil {
		t.Fatal("run(--no-such-flag) = nil, want an error")
	}
}
