// Package deploy_test reads the shipped service definitions as data. They are constants —
// everything that varies between installations is in a file ansible-hosts writes — so what
// can be tested is what they name.
package deploy_test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const (
	unit       = "systemd/prismotr.service"
	updateUnit = "systemd/prismotr-update.service"
	timerUnit  = "systemd/prismotr-update.timer"
	envExample = "prismotr.env.example"
)

// keptScript is where a host keeps the install script its update timer runs.
const keptScript = "/usr/local/libexec/prismotr/prismotr-install.sh"

// shipped is every file this directory installs on a host, examples included.
var shipped = []string{unit, updateUnit, timerUnit, envExample}

// layout is the spec's table read from the other side: the paths each unit has to name.
var layout = []struct {
	name  string
	file  string
	paths []string
}{
	{"unit", unit, []string{"/usr/local/bin/prismotr", "/etc/prismotr/prismotr.yaml", "/etc/prismotr/prismotr.env"}},
	{"update unit", updateUnit, []string{keptScript}},
	{"update timer", timerUnit, nil},
}

// allowedNames is every dotted name a shipped file may contain. Anything else is a host, a
// domain or a secret that someone pasted in.
var allowedNames = map[string]bool{
	"prismotr.yaml":         true,
	"prismotr.env":          true,
	"prismotr-install.sh":   true,
	"network-online.target": true,
	"multi-user.target":     true,
	"timers.target":         true,
	"installer.md":          true,
	"config.md":             true,
}

var (
	absolutePath = regexp.MustCompile(`(^|[^A-Za-z0-9._/<-])(/[A-Za-z0-9._-]+(?:/[A-Za-z0-9._-]+)*)`)
	assignment   = regexp.MustCompile(`(?m)^([A-Z][A-Z0-9_]*)=(.*)$`)
	restartDelay = regexp.MustCompile(`(?m)^RestartSec=[1-9][0-9]*(ms|s|min)?$`)
	urlLiteral   = regexp.MustCompile(`[a-z][a-z0-9+.-]*://[^\s"'<]+`)
	ipAddress    = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}\b`)
	dottedName   = regexp.MustCompile(`[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)+`)
	letter       = regexp.MustCompile(`[A-Za-z]`)
	comment      = regexp.MustCompile(`(?m)^[ \t]*#.*?$`)
)

func read(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(body)
}

// code is the file with its comments removed: a path a unit only mentions in a comment is a
// path the service never uses.
func code(t *testing.T, name string) string {
	t.Helper()
	return comment.ReplaceAllString(read(t, name), "")
}

// spec: installer.md#the-layout — every path is fixed there, which is what lets a unit be a
// constant.
func TestUnitsNameThePathsTheSpecFixes(t *testing.T) {
	for _, u := range layout {
		t.Run(u.name, func(t *testing.T) {
			body := code(t, u.file)
			for _, want := range u.paths {
				if !strings.Contains(body, want) {
					t.Errorf("%s does not name %s", u.file, want)
				}
			}
		})
	}
}

// spec: installer.md#the-layout — a path outside the table is one the installer does not know.
func TestUnitsNameNoPathTheSpecDoesNotFix(t *testing.T) {
	for _, u := range layout {
		t.Run(u.name, func(t *testing.T) {
			allowed := map[string]bool{}
			for _, path := range u.paths {
				allowed[path] = true
				allowed[filepath.Dir(path)] = true
			}
			for _, match := range absolutePath.FindAllStringSubmatch(code(t, u.file), -1) {
				if !allowed[match[2]] {
					t.Errorf("%s names %s, which the layout table does not fix", u.file, match[2])
				}
			}
		})
	}
}

func serviceCommand(t *testing.T, file string) []string {
	t.Helper()
	for _, line := range strings.Split(code(t, file), "\n") {
		if command, found := strings.CutPrefix(line, "ExecStart="); found {
			return strings.Fields(command)
		}
	}
	t.Fatalf("%s has no ExecStart", file)
	return nil
}

// spec: installer.md#the-layout — the service starts the binary with the configuration file
// and nothing else: the listen address comes from the optional environment file.
func TestTheUnitStartsTheBinaryWithItsConfiguration(t *testing.T) {
	want := []string{"/usr/local/bin/prismotr", "--config", "/etc/prismotr/prismotr.yaml"}
	if got := serviceCommand(t, unit); !slices.Equal(got, want) {
		t.Errorf("%s starts %q, want %q", unit, got, want)
	}
	if !strings.Contains(code(t, unit), "EnvironmentFile=-/etc/prismotr/prismotr.env") {
		t.Errorf("%s does not load the environment file as optional", unit)
	}
}

// AGENTS.md, public repository: no file this repository ships names a host, a URL or a secret.
func TestNoShippedFileCarriesAHostAddressOrSecret(t *testing.T) {
	for _, file := range shipped {
		t.Run(file, func(t *testing.T) {
			body := read(t, file)
			if found := ipAddress.FindString(body); found != "" && found != "127.0.0.1" {
				t.Errorf("%s carries the address %s", file, found)
			}
			if found := urlLiteral.FindString(body); found != "" {
				t.Errorf("%s carries the URL %s", file, found)
			}
			for _, found := range dottedName.FindAllString(body, -1) {
				if !letter.MatchString(found) || allowedNames[found] {
					continue
				}
				t.Errorf("%s names %s, which reads like a host or a secret", file, found)
			}
		})
	}
}

// The example shows the one key and leaves it commented out: the default is the product's.
func TestTheExampleEnvironmentFileSetsNoValue(t *testing.T) {
	body := read(t, envExample)
	if !strings.Contains(body, "PRISMOTR_LISTEN") {
		t.Errorf("%s does not mention PRISMOTR_LISTEN", envExample)
	}
	if matches := assignment.FindAllStringSubmatch(body, -1); len(matches) != 0 {
		t.Errorf("%s assigns %v; every value is a deployment setting", envExample, matches)
	}
}

// spec: installer.md#the-layout — the daemon runs as the unprivileged prismotr account.
func TestTheUnitRunsAsTheAccount(t *testing.T) {
	body := read(t, unit)
	for _, want := range []string{"User=prismotr", "Group=prismotr"} {
		if !strings.Contains(body, want) {
			t.Errorf("%s has no %s", unit, want)
		}
	}
}

// The daemon is restarted after a short delay, however fast it keeps failing, and comes back
// after a reboot without anyone logging in.
func TestTheUnitRestartsAndComesBackAfterAReboot(t *testing.T) {
	body := code(t, unit)
	for _, want := range []string{
		"Restart=always", "StartLimitIntervalSec=0",
		"[Install]", "WantedBy=multi-user.target", "Wants=network-online.target", "After=network-online.target",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("%s has no %s", unit, want)
		}
	}
	if !restartDelay.MatchString(body) {
		t.Errorf("%s sets no RestartSec with a delay in it", unit)
	}
}

// spec: installer.md#the-handover — the update unit runs the kept script as root, once, after
// the network is up; the command line is part of what a kept script depends on.
func TestTheUpdateUnitRunsTheKeptScriptOnce(t *testing.T) {
	body := code(t, updateUnit)
	want := []string{keptScript, "--follow-target"}
	if got := serviceCommand(t, updateUnit); !slices.Equal(got, want) {
		t.Errorf("%s starts %q, want %q", updateUnit, got, want)
	}
	for _, line := range []string{"Type=oneshot", "Wants=network-online.target", "After=network-online.target"} {
		if !strings.Contains(body, line) {
			t.Errorf("%s has no %s", updateUnit, line)
		}
	}
	for _, unwanted := range []string{"User=", "Group=", "SuccessExitStatus=", "StandardOutput=", "StandardError=", "[Install]", "prismotr.service"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("%s sets %s", updateUnit, unwanted)
		}
	}
}

// Once an hour, spread over ten minutes, caught up after the host was down, armed on boot.
func TestTheTimerRunsTheUpdateHourly(t *testing.T) {
	lines := map[string]bool{}
	keys := map[string]int{}
	for _, line := range strings.Split(code(t, timerUnit), "\n") {
		line = strings.TrimSpace(line)
		lines[line] = true
		if key, _, found := strings.Cut(line, "="); found {
			keys[key]++
		}
	}
	for _, line := range []string{"OnCalendar=hourly", "RandomizedDelaySec=10min", "Persistent=true", "[Install]", "WantedBy=timers.target"} {
		if !lines[line] {
			t.Errorf("%s has no line %s", timerUnit, line)
		}
		if key, _, found := strings.Cut(line, "="); found && keys[key] != 1 {
			t.Errorf("%s sets %s %d times, want once", timerUnit, key, keys[key])
		}
	}
	if keys["OnBootSec"] != 0 {
		t.Errorf("%s sets OnBootSec=; the update runs at boot only when a run was missed", timerUnit)
	}
}
