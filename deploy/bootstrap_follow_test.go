// This file drives the real prismotr-install.sh as the update timer runs it: against a
// synthetic origin holding more than one release, whose installers answer as a test scripts
// them to.
package deploy_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// How a scripted installer answers a follow hand-over. Any other value is written into the
// answer as it stands, which is how a version or a malformed answer is scripted.
const (
	answersNothing  = ""
	asksForBinary   = "install"
	asksEvenWithIt  = "greedy"
	refusesHandOver = "refuse"
)

// answeringArchive is an installer that records how it was called, the binary it was handed
// and what arrived on its stdin, then answers as behaviour says.
func answeringArchive(t *testing.T, behaviour string) []byte {
	t.Helper()
	script := "#!/bin/sh\nprintf 'args: %s\\n' \"$*\" >> \"$RECORD\"\n" +
		"release= answer= binary=\n" +
		"while [ $# -gt 0 ]; do\n  case $1 in\n" +
		"  --release) release=$2 ;;\n  --answer) answer=$2 ;;\n  --binary) binary=$2 ;;\n" +
		"  esac\n  shift\ndone\n" +
		"[ -z \"$binary\" ] || printf 'binary: %s\\n' \"$(cat \"$binary\")\" >> \"$RECORD\"\n" +
		"printf 'stdin: [%s]\\n' \"$(cat)\" >> \"$RECORD\"\n"
	switch behaviour {
	case answersNothing:
	case asksForBinary:
		script += "[ -n \"$binary\" ] || printf '%s\\n' \"$release\" > \"$answer\"\n"
	case asksEvenWithIt:
		script += "printf '%s\\n' \"$release\" > \"$answer\"\n"
	case refusesHandOver:
		script += "echo 'install.sh: --binary is required' >&2\nexit 1\n"
	default:
		script += "printf '" + behaviour + "\\n' > \"$answer\"\n"
	}
	script += "exit 0\n"
	return packInstaller(t, map[string]string{"install.sh": script}, nil)
}

func followOrigin(t *testing.T, behaviour string) *origin {
	t.Helper()
	o := newOrigin(t)
	o.set("prismotr-installer-1.2.3.tar.gz", answeringArchive(t, behaviour))
	o.sign(t)
	return o
}

func binaryDigest(version string) string {
	sum := sha256.Sum256(syntheticBinary(version))
	return hex.EncodeToString(sum[:])
}

func handOvers(t *testing.T, run bootstrapRun) []string {
	t.Helper()
	body, err := os.ReadFile(run.record)
	if err != nil {
		return nil
	}
	var calls []string
	for _, line := range strings.Split(string(body), "\n") {
		if call, ok := strings.CutPrefix(line, "args: "); ok {
			calls = append(calls, call)
		}
	}
	return calls
}

func fetchedTags(o *origin) []string {
	var tags []string
	for _, path := range o.requests() {
		rest, ok := strings.CutPrefix(path, "/releases/download/")
		if !ok {
			continue
		}
		tag, _, _ := strings.Cut(rest, "/")
		if len(tags) == 0 || tags[len(tags)-1] != tag {
			tags = append(tags, tag)
		}
	}
	return tags
}

func fetchedCount(o *origin, version, name string) int {
	count := 0
	for _, path := range o.requests() {
		if path == "/releases/download/v"+version+"/"+name {
			count++
		}
	}
	return count
}

// spec: installer.md#following-a-target — a panel already at its target costs no binary: the
// newest release's installer is handed the five options and no stdin, answers nothing, and
// the run adds no claim of its own.
func TestAFollowRunDownloadsNoBinaryForAPanelAtItsTarget(t *testing.T) {
	o := followOrigin(t, answersNothing)
	run := newBootstrapRun(t, o, "--follow-target")
	run.stdin = "nothing the installer should read"

	stdout, stderr, err := run.start(t)
	if err != nil {
		t.Fatalf("the run failed: %v\n%s%s", err, stdout, stderr)
	}

	calls := handOvers(t, run)
	if len(calls) != 1 {
		t.Fatalf("the run handed over %d times, want once: %v", len(calls), calls)
	}
	for _, want := range []string{"--follow-target", "--release 1.2.3", "--newest 1.2.3", "--digest " + binaryDigest("1.2.3"), "--answer "} {
		if !strings.Contains(calls[0], want) {
			t.Errorf("the hand-over does not carry %q: %s", want, calls[0])
		}
	}
	if strings.Contains(calls[0], "--binary") {
		t.Errorf("the first hand-over carries a binary: %s", calls[0])
	}
	if n := fetchedCount(o, "1.2.3", binaryName("1.2.3")); n != 0 {
		t.Errorf("the run downloaded the binary %d times; nobody asked for it", n)
	}
	if record, _ := os.ReadFile(run.record); !strings.Contains(string(record), "stdin: []") {
		t.Errorf("the installer was handed something on stdin:\n%s", record)
	}
	if strings.Contains(stdout, "installing") {
		t.Errorf("the run claims an install of its own:\n%s", stdout)
	}
}

// spec: installer.md#following-a-target — an installer asking for its own release's binary
// gets it once, checked against the manifest already verified, with the same options.
func TestAFollowRunDownloadsTheBinaryWhenAsked(t *testing.T) {
	o := followOrigin(t, asksForBinary)
	run := newBootstrapRun(t, o, "--follow-target")

	stdout, stderr, err := run.start(t)
	if err != nil {
		t.Fatalf("the run failed: %v\n%s%s", err, stdout, stderr)
	}

	calls := handOvers(t, run)
	if len(calls) != 2 {
		t.Fatalf("the run handed over %d times, want twice: %v", len(calls), calls)
	}
	for _, want := range []string{"--release 1.2.3", "--newest 1.2.3", "--digest " + binaryDigest("1.2.3"), "--binary "} {
		if !strings.Contains(calls[1], want) {
			t.Errorf("the second hand-over does not carry %q: %s", want, calls[1])
		}
	}
	if record, _ := os.ReadFile(run.record); !strings.Contains(string(record), "binary: #!/bin/sh\necho prismotr 1.2.3") {
		t.Errorf("the installer was not handed its release's binary:\n%s", record)
	}
	if n := fetchedCount(o, "1.2.3", binaryName("1.2.3")); n != 1 {
		t.Errorf("the run downloaded the binary %d times, want once", n)
	}
	if n := fetchedCount(o, "1.2.3", "SHA256SUMS"); n != 1 {
		t.Errorf("the run downloaded the manifest %d times; the binary is checked against the one verified", n)
	}
}

// spec: installer.md#following-a-target — a named older release is fetched after the newest,
// its own installer is handed its own digest and the same newest version, and only its binary
// is downloaded; the downgrade guard does not stand in the way of a target.
func TestAFollowRunFetchesTheReleaseTheTargetNames(t *testing.T) {
	o := followOrigin(t, "1.0.0")
	o.publish(t, "1.0.0", answeringArchive(t, asksForBinary))
	run := newBootstrapRun(t, o, "--follow-target")
	installBinary(t, run.destDir, "#!/bin/sh\necho prismotr 1.2.3\n")

	stdout, stderr, err := run.start(t)
	if err != nil {
		t.Fatalf("the run failed: %v\n%s%s", err, stdout, stderr)
	}

	calls := handOvers(t, run)
	if len(calls) != 3 {
		t.Fatalf("the run handed over %d times, want three times: %v", len(calls), calls)
	}
	for _, want := range []string{"--release 1.0.0", "--newest 1.2.3", "--digest " + binaryDigest("1.0.0")} {
		if !strings.Contains(calls[1], want) || !strings.Contains(calls[2], want) {
			t.Errorf("the hand-overs to 1.0.0 do not both carry %q: %v", want, calls[1:])
		}
	}
	if strings.Contains(calls[1], "--binary") || !strings.Contains(calls[2], "--binary") {
		t.Errorf("only the last hand-over carries a binary: %v", calls)
	}
	if record, _ := os.ReadFile(run.record); !strings.Contains(string(record), "binary: #!/bin/sh\necho prismotr 1.0.0") {
		t.Errorf("the named release's installer was not handed its binary:\n%s", record)
	}
	if n := fetchedCount(o, "1.2.3", binaryName("1.2.3")); n != 0 {
		t.Errorf("the newest release's binary was downloaded %d times; nobody asked for it", n)
	}
	if tags := fetchedTags(o); strings.Join(tags, " ") != "v1.2.3 v1.0.0" {
		t.Errorf("the run fetched %v, want the newest release and then the named one", tags)
	}
}

// spec: installer.md#following-a-target — a panel held on an older release that it already
// runs costs no binary either.
func TestAFollowRunDownloadsNoBinaryForAPanelPinnedWhereItIs(t *testing.T) {
	o := followOrigin(t, "1.0.0")
	o.publish(t, "1.0.0", answeringArchive(t, answersNothing))
	run := newBootstrapRun(t, o, "--follow-target")

	stdout, stderr, err := run.start(t)
	if err != nil {
		t.Fatalf("the run failed: %v\n%s%s", err, stdout, stderr)
	}
	if calls := handOvers(t, run); len(calls) != 2 {
		t.Errorf("the run handed over %d times, want twice: %v", len(calls), calls)
	}
	for _, version := range []string{"1.2.3", "1.0.0"} {
		if n := fetchedCount(o, version, binaryName(version)); n != 0 {
			t.Errorf("the run downloaded %s's binary %d times; nobody asked for it", version, n)
		}
	}
}

// spec: installer.md#following-a-target — an origin serving an older release as the newest
// cannot roll the panel back.
func TestAFollowRunRefusesANewestOlderThanTheBinaryInPlace(t *testing.T) {
	o := followOrigin(t, asksForBinary)
	run := newBootstrapRun(t, o, "--follow-target")
	installBinary(t, run.destDir, "#!/bin/sh\necho prismotr 9.9.9\n")

	stdout, stderr, err := run.start(t)

	if err == nil {
		t.Fatalf("the run succeeded; 1.2.3 is older than 9.9.9\n%s%s", stdout, stderr)
	}
	if !strings.Contains(stderr, "9.9.9") || !strings.Contains(stderr, "a follow run installs nothing") {
		t.Errorf("the refusal does not name the version in place as a follow run's:\n%s", stderr)
	}
	if calls := handOvers(t, run); len(calls) != 0 {
		t.Errorf("the run still handed over: %v", calls)
	}
}

// spec: installer.md#following-a-target — the ways an answer cannot be followed, none of
// which installs anything.
func TestAFollowRunThatCannotFollowTheAnswer(t *testing.T) {
	tests := []struct {
		name      string
		newest    string
		prepare   func(t *testing.T, o *origin)
		says      []string
		handOvers int
		fetched   string
	}{
		{name: "a release that predates the installer", newest: "1.0.0",
			prepare: func(t *testing.T, o *origin) { o.publish(t, "1.0.0", nil) },
			says:    []string{"predates the installer"}, handOvers: 1, fetched: "v1.2.3 v1.0.0"},
		{name: "a release whose signature does not verify", newest: "1.0.0",
			prepare: func(t *testing.T, o *origin) {
				o.publish(t, "1.0.0", answeringArchive(t, asksForBinary))
				o.tamper("1.0.0", "SHA256SUMS.sig", []byte("not a signature\n"))
			},
			says: []string{"signature"}, handOvers: 1, fetched: "v1.2.3 v1.0.0"},
		{name: "a release whose installer does not take this hand-over", newest: "1.0.0",
			prepare: func(t *testing.T, o *origin) { o.publish(t, "1.0.0", answeringArchive(t, refusesHandOver)) },
			says:    []string{"--binary is required"}, handOvers: 2, fetched: "v1.2.3 v1.0.0"},
		{name: "a release the origin does not serve", newest: "7.7.7",
			says: []string{"7.7.7"}, handOvers: 1, fetched: "v1.2.3 v7.7.7"},
		{name: "an installer that names another version in turn", newest: "1.0.0",
			prepare: func(t *testing.T, o *origin) { o.publish(t, "1.0.0", answeringArchive(t, "1.1.0")) },
			says:    []string{"1.0.0", "1.1.0"}, handOvers: 2, fetched: "v1.2.3 v1.0.0"},
		{name: "a binary that is not the one its manifest names", newest: asksForBinary,
			prepare: func(_ *testing.T, o *origin) {
				o.tamper("1.2.3", binaryName("1.2.3"), []byte("#!/bin/sh\necho tampered\n"))
			},
			says: []string{"does not match the digest"}, handOvers: 1, fetched: "v1.2.3"},
		{name: "a named release's binary that is not the one its manifest names", newest: "1.0.0",
			prepare: func(t *testing.T, o *origin) {
				o.publish(t, "1.0.0", answeringArchive(t, asksForBinary))
				o.tamper("1.0.0", binaryName("1.0.0"), []byte("#!/bin/sh\necho tampered\n"))
			},
			says: []string{"does not match the digest"}, handOvers: 2, fetched: "v1.2.3 v1.0.0"},
		{name: "an installer that answers after it was given its binary", newest: asksEvenWithIt,
			says: []string{"after it was given its binary"}, handOvers: 2, fetched: "v1.2.3"},
		{name: "a named release's installer that answers after it was given its binary", newest: "1.0.0",
			prepare: func(t *testing.T, o *origin) { o.publish(t, "1.0.0", answeringArchive(t, asksEvenWithIt)) },
			says:    []string{"after it was given its binary"}, handOvers: 3, fetched: "v1.2.3 v1.0.0"},
		{name: "an answer that is not a version", newest: "stable", says: []string{"not a version: stable"}, handOvers: 1, fetched: "v1.2.3"},
		{name: "an answer that is half a version", newest: "1.2", says: []string{"not a version: 1.2"}, handOvers: 1, fetched: "v1.2.3"},
		{name: "an answer of two versions", newest: "1.0.0\\n1.1.0", says: []string{"not a version"}, handOvers: 1, fetched: "v1.2.3"},
		{name: "an answer with a second, empty line", newest: "1.0.0\\n", says: []string{"not a version"}, handOvers: 1, fetched: "v1.2.3"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			o := followOrigin(t, test.newest)
			if test.prepare != nil {
				test.prepare(t, o)
			}
			run := newBootstrapRun(t, o, "--follow-target")

			stdout, stderr, err := run.start(t)

			if err == nil {
				t.Fatalf("the run succeeded; it had to refuse\n%s%s", stdout, stderr)
			}
			for _, want := range test.says {
				if !strings.Contains(stderr, want) {
					t.Errorf("the refusal does not say %q:\n%s", want, stderr)
				}
			}
			if calls := handOvers(t, run); len(calls) != test.handOvers {
				t.Errorf("the run handed over %d times, want %d: %v", len(calls), test.handOvers, calls)
			}
			if tags := strings.Join(fetchedTags(o), " "); tags != test.fetched {
				t.Errorf("the run fetched from %q, want %q", tags, test.fetched)
			}
		})
	}
}

// spec: installer.md#following-a-target — the whole chain with the installer a release really
// carries: the target decides which release lands, and the next run downloads no binary for a
// panel already there.
func TestTheWholeChainFollowsTheTarget(t *testing.T) {
	for _, test := range []struct{ target, version string }{
		{"latest\n", "1.2.3"},
		{"1.2.3\n", "1.2.3"},
		{"1.0.0\n", "1.0.0"},
	} {
		t.Run(strings.TrimSpace(test.target), func(t *testing.T) {
			o := newOrigin(t)
			o.set("prismotr-installer-1.2.3.tar.gz", realArchive(t))
			o.sign(t)
			o.publish(t, "1.0.0", realArchive(t))
			run := newBootstrapRun(t, o, "--follow-target")
			writeTarget(t, run.destDir, test.target)

			stdout, stderr, err := run.start(t)
			if err != nil {
				t.Fatalf("the chain failed: %v\n%s%s", err, stdout, stderr)
			}

			installed, err := os.ReadFile(filepath.Join(run.destDir, "usr/local/bin/prismotr"))
			if err != nil {
				t.Fatalf("no binary landed: %v\n%s%s", err, stdout, stderr)
			}
			if want := "prismotr " + test.version; !strings.Contains(string(installed), want) {
				t.Errorf("the binary that landed is %q, want %s", installed, want)
			}

			stdout, stderr, err = run.start(t)
			if err != nil {
				t.Fatalf("the second run failed: %v\n%s%s", err, stdout, stderr)
			}
			if n := fetchedCount(o, test.version, binaryName(test.version)); n != 1 {
				t.Errorf("two runs downloaded the binary %d times, want once", n)
			}
			if !strings.Contains(stdout, "already") {
				t.Errorf("the second run does not say the panel is already there:\n%s", stdout)
			}
		})
	}
}

// spec: installer.md#following-a-target — a binary in place that is already the release its
// target resolves to, but a service definition that differs, is finished without a second
// download of that binary.
func TestAFollowRunDownloadsNoBinaryToFinishAPanelAtItsTarget(t *testing.T) {
	o := newOrigin(t)
	o.set("prismotr-installer-1.2.3.tar.gz", realArchive(t))
	o.sign(t)
	run := newBootstrapRun(t, o, "--follow-target")
	writeTarget(t, run.destDir, "latest\n")
	if stdout, stderr, err := run.start(t); err != nil {
		t.Fatalf("the first run failed: %v\n%s%s", err, stdout, stderr)
	}
	unitFile := filepath.Join(run.destDir, "etc/systemd/system/prismotr.service")
	if err := os.WriteFile(unitFile, []byte("# an older unit\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := run.start(t)
	if err != nil {
		t.Fatalf("the second run failed: %v\n%s%s", err, stdout, stderr)
	}
	if n := fetchedCount(o, "1.2.3", binaryName("1.2.3")); n != 1 {
		t.Errorf("two runs downloaded the binary %d times, want once", n)
	}
	if body, _ := os.ReadFile(unitFile); string(body) == "# an older unit\n" {
		t.Errorf("the service definition was not installed:\n%s", stdout)
	}
}
