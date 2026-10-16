// This file drives the real install.sh, the installer a release carries, as an operator's run
// and as a follow hand-over. Every run is staged under DESTDIR, so the machine running the
// test is never touched.
package deploy_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const installer = "./install.sh"

type installedFile struct {
	path string
	mode os.FileMode
}

// installLayout is the spec's layout table for what the installer writes.
// spec: installer.md#the-layout
func installLayout() []installedFile {
	return []installedFile{
		{"usr/local/bin/prismotr", 0o755},
		{"etc/systemd/system/prismotr.service", 0o644},
		{"etc/prismotr/prismotr.yaml.example", 0o644},
		{"etc/prismotr/prismotr.env.example", 0o644},
	}
}

type installRun struct {
	destDir string
	args    []string
	pathDir string // prepended to PATH, to catch a service command being run
	umask   string
}

func (r installRun) start(t *testing.T) (stdout, stderr string, err error) {
	t.Helper()
	path := os.Getenv("PATH")
	if r.pathDir != "" {
		path = r.pathDir + string(os.PathListSeparator) + path
	}
	command := exec.Command(installer, r.args...)
	if r.umask != "" {
		shell := []string{"-c", "umask " + r.umask + `; exec "$0" "$@"`, installer}
		command = exec.Command("/bin/sh", append(shell, r.args...)...)
	}
	command.Env = []string{"PATH=" + path, "DESTDIR=" + r.destDir}
	var out, errs bytes.Buffer
	command.Stdout, command.Stderr = &out, &errs
	err = command.Run()
	return out.String(), errs.String(), err
}

func (r installRun) mustRun(t *testing.T) (stdout, stderr string) {
	t.Helper()
	stdout, stderr, err := r.start(t)
	if err != nil {
		t.Fatalf("install.sh %v: %v\nstdout: %s\nstderr: %s", r.args, err, stdout, stderr)
	}
	return stdout, stderr
}

// binary is what the script copies: a small executable, not a built daemon. Its mode is
// deliberately not the layout's, so a run that merely copied it across would fail.
func binary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "prismotr")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho prismotr 1.2.3\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func install(t *testing.T, destDir string, extra ...string) (stdout, stderr string) {
	t.Helper()
	args := append([]string{"--binary", binary(t)}, extra...)
	return installRun{destDir: destDir, args: args}.mustRun(t)
}

func assertLayout(t *testing.T, destDir string, present ...string) {
	t.Helper()
	got := tree(t, destDir)
	if want := len(installLayout()) + len(present); len(got) != want {
		t.Errorf("found %d files, want %d: %v", len(got), want, sortedKeys(got))
	}
	for _, file := range installLayout() {
		info, err := os.Stat(filepath.Join(destDir, file.path))
		if err != nil {
			t.Errorf("%s: %v", file.path, err)
			continue
		}
		if info.Mode().Perm() != file.mode {
			t.Errorf("%s has mode %o, want %o", file.path, info.Mode().Perm(), file.mode)
		}
	}
}

func tree(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	found := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		found[rel] = body
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return found
}

func sortedKeys(files map[string][]byte) []string {
	return slices.Sorted(maps.Keys(files))
}

func writeConfig(t *testing.T, destDir string) {
	t.Helper()
	dir := filepath.Join(destDir, "etc", "prismotr")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prismotr.yaml"), []byte("# ansible's own\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func chmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// spec: installer.md#installing — a host with no panel gets the binary, the service and the
// examples, and the run says which configuration is still missing.
func TestAFreshInstallWritesTheLayoutAndNamesWhatIsMissing(t *testing.T) {
	destDir := t.TempDir()

	stdout, _ := install(t, destDir)

	assertLayout(t, destDir)
	for _, want := range []string{"prismotr.yaml", "install -o root -g root -m 0644"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the run does not name %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "prismotr.env\n") {
		t.Errorf("the run asks for the optional environment file:\n%s", stdout)
	}
}

// spec: installer.md#installing — with the configuration present the run says the service is
// what to look at, not what to create.
func TestAnInstallWithConfigurationSaysTheServiceIsRunning(t *testing.T) {
	destDir := t.TempDir()
	writeConfig(t, destDir)

	stdout, _ := install(t, destDir)

	if strings.Contains(stdout, "will not start until") {
		t.Errorf("the run reports missing configuration it was given:\n%s", stdout)
	}
	if !strings.Contains(stdout, "systemctl status prismotr.service") {
		t.Errorf("the run does not name the service command:\n%s", stdout)
	}
}

// spec: installer.md#installing — a re-run replaces the binary and leaves ansible's
// configuration exactly as it was.
func TestARerunReplacesTheBinaryAndKeepsConfiguration(t *testing.T) {
	destDir := t.TempDir()
	writeConfig(t, destDir)
	install(t, destDir)

	newer := filepath.Join(t.TempDir(), "prismotr")
	if err := os.WriteFile(newer, []byte("#!/bin/sh\necho prismotr 9.9.9\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	installRun{destDir: destDir, args: []string{"--binary", newer}}.mustRun(t)

	installed, _ := os.ReadFile(filepath.Join(destDir, "usr/local/bin/prismotr"))
	if !strings.Contains(string(installed), "9.9.9") {
		t.Errorf("the binary was not replaced: %s", installed)
	}
	config, _ := os.ReadFile(filepath.Join(destDir, "etc/prismotr/prismotr.yaml"))
	if string(config) != "# ansible's own\n" {
		t.Errorf("the configuration was rewritten: %s", config)
	}
}

// spec: installer.md#staged-installs — a staged run registers nothing: no service command is
// on the PATH it is given, and calling one would be visible.
func TestAStagedRunCallsNoServiceCommand(t *testing.T) {
	destDir := t.TempDir()
	pathDir := t.TempDir()
	marker := filepath.Join(pathDir, "called")
	for _, name := range []string{"systemctl", "useradd", "adduser"} {
		body := "#!/bin/sh\necho " + name + " \"$@\" >> " + marker + "\n"
		if err := os.WriteFile(filepath.Join(pathDir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	installRun{destDir: destDir, args: []string{"--binary", binary(t)}, pathDir: pathDir}.mustRun(t)

	if body, err := os.ReadFile(marker); err == nil {
		t.Errorf("a staged run called: %s", body)
	}
}

// spec: installer.md#installing — a directory the layout owns must not be a symlink somebody
// else planted.
func TestAnInstallRefusesASymlinkedDirectory(t *testing.T) {
	destDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(destDir, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(destDir, "etc", "prismotr")); err != nil {
		t.Fatal(err)
	}

	_, stderr, err := installRun{destDir: destDir, args: []string{"--binary", binary(t)}}.start(t)

	if err == nil {
		t.Fatal("the run succeeded; a symlinked directory had to stop it")
	}
	if !strings.Contains(stderr, "symlink") {
		t.Errorf("the refusal does not name the cause:\n%s", stderr)
	}
}

// spec: installer.md#staged-installs — DESTDIR is a staging prefix, and a value that stages
// nothing is refused rather than quietly writing into the real system.
func TestAnInstallRefusesADestdirThatStagesNothing(t *testing.T) {
	for _, destDir := range []string{"/", "//", "relative/path", "/.", "/etc/..", "/tmp/../"} {
		t.Run(destDir, func(t *testing.T) {
			_, stderr, err := installRun{destDir: destDir, args: []string{"--binary", binary(t)}}.start(t)
			if err == nil {
				t.Fatal("the run succeeded; this DESTDIR had to stop it")
			}
			if !strings.Contains(stderr, "DESTDIR") {
				t.Errorf("the refusal does not name DESTDIR:\n%s", stderr)
			}
		})
	}
}

// spec: installer.md#installing — the refusals a run makes before it writes anything.
func TestAnInstallRefusalWritesNothing(t *testing.T) {
	notExecutable := filepath.Join(t.TempDir(), "prismotr")
	if err := os.WriteFile(notExecutable, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		args  []string
		names string
	}{
		{"no binary", nil, "--binary"},
		{"a binary that is not there", []string{"--binary", "/no/such/prismotr"}, "no file"},
		{"a binary that is not executable", []string{"--binary", notExecutable}, "not executable"},
		{"an unknown option", []string{"--binary", "x", "--wat"}, "--wat"},
		{"an option without its value", []string{"--binary"}, "--binary"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			destDir := t.TempDir()
			_, stderr, err := installRun{destDir: destDir, args: test.args}.start(t)
			if err == nil {
				t.Fatal("the run succeeded; it had to refuse")
			}
			if !strings.Contains(stderr, test.names) {
				t.Errorf("the refusal does not name %q:\n%s", test.names, stderr)
			}
			if files := tree(t, destDir); len(files) != 0 {
				t.Errorf("a refusal wrote files: %v", sortedKeys(files))
			}
		})
	}
}

// spec: installer.md#installing — the examples are the release's, so a run replaces them.
func TestAnInstallOverwritesTheExamples(t *testing.T) {
	destDir := t.TempDir()
	install(t, destDir)
	example := filepath.Join(destDir, "etc/prismotr/prismotr.yaml.example")
	if err := os.WriteFile(example, []byte("edited by hand\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	install(t, destDir)

	if body, _ := os.ReadFile(example); strings.Contains(string(body), "edited by hand") {
		t.Errorf("the example was not replaced: %s", body)
	}
}

// spec: installer.md#the-layout — the directory is created with the table's mode, on a staged
// run as on a real one, and one left wide open is narrowed; the modes do not follow the
// caller's umask.
func TestAnInstallFixesTheDirectoryModeWhateverTheUmask(t *testing.T) {
	destDir := t.TempDir()
	wide := filepath.Join(destDir, "etc", "prismotr")
	if err := os.MkdirAll(wide, 0o777); err != nil {
		t.Fatal(err)
	}
	chmod(t, wide, 0o777)

	installRun{destDir: destDir, args: []string{"--binary", binary(t)}, umask: "000"}.mustRun(t)

	assertLayout(t, destDir)
	info, err := os.Stat(wide)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("etc/prismotr has mode %o, want 755", info.Mode().Perm())
	}
}

func TestInstallerHelpGoesToStdout(t *testing.T) {
	stdout, stderr, err := installRun{destDir: t.TempDir(), args: []string{"-h"}}.start(t)
	if err != nil {
		t.Fatalf("-h failed: %v\n%s%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "usage:") || stderr != "" {
		t.Errorf("the usage is not on stdout alone:\nstdout: %s\nstderr: %s", stdout, stderr)
	}
}

// ---- Answering a follow run ----

type followRun struct {
	destDir string
	release string
	newest  string
	answer  string
	binary  string
	digest  string
}

func newFollowRun(t *testing.T, target, release, newest string) followRun {
	t.Helper()
	f := followRun{
		destDir: t.TempDir(),
		release: release,
		newest:  newest,
		answer:  filepath.Join(t.TempDir(), "answer"),
		binary:  binary(t),
	}
	f.digest = fileDigest(t, f.binary)
	if err := os.WriteFile(f.answer, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if target != "" {
		writeTarget(t, f.destDir, target)
	}
	return f
}

func fileDigest(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func writeTarget(t *testing.T, destDir, body string) {
	t.Helper()
	dir := filepath.Join(destDir, "etc", "prismotr")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "target"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f followRun) args() []string {
	return []string{"--follow-target", "--release", f.release, "--newest", f.newest, "--digest", f.digest, "--answer", f.answer}
}

func (f followRun) withBinary() []string { return append(f.args(), "--binary", f.binary) }

func (f followRun) start(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	if args == nil {
		args = f.args()
	}
	return installRun{destDir: f.destDir, args: args}.start(t)
}

func (f followRun) mustStart(t *testing.T, args ...string) string {
	t.Helper()
	stdout, stderr, err := f.start(t, args...)
	if err != nil {
		t.Fatalf("the run failed: %v\n%s%s", err, stdout, stderr)
	}
	return stdout
}

func (f followRun) answered(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(f.answer)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func (f followRun) installed(t *testing.T) {
	t.Helper()
	installRun{destDir: f.destDir, args: []string{"--binary", f.binary}}.mustRun(t)
}

// spec: installer.md#answering-a-follow-run — a target that resolves to the release handed
// over, with its binary, installs it, and the answer stays empty.
func TestAFollowRunInstallsTheReleaseItsTargetNames(t *testing.T) {
	for _, target := range []string{"1.2.3\n", "1.2.3"} {
		t.Run(strings.TrimSpace(target), func(t *testing.T) {
			f := newFollowRun(t, target, "1.2.3", "1.3.0")
			f.mustStart(t, f.withBinary()...)
			assertLayout(t, f.destDir, "etc/prismotr/target")
			if got := f.answered(t); got != "" {
				t.Errorf("the answer holds %q; an installer that installs leaves it empty", got)
			}
		})
	}
}

// spec: installer.md#answering-a-follow-run — without the binary, a target that resolves to
// the release handed over asks for that release's binary, and nothing is written.
func TestAFollowRunAsksForItsOwnReleasesBinary(t *testing.T) {
	f := newFollowRun(t, "1.2.3\n", "1.2.3", "1.3.0")
	f.mustStart(t)
	if got := strings.TrimSpace(f.answered(t)); got != "1.2.3" {
		t.Errorf("the answer holds %q, want 1.2.3", got)
	}
	if files := sortedKeys(tree(t, f.destDir)); len(files) != 1 {
		t.Errorf("an answering run wrote files: %v", files)
	}
}

// spec: installer.md#answering-a-follow-run — target latest resolves to --newest.
func TestAFollowRunResolvesLatestToTheNewestRelease(t *testing.T) {
	for _, test := range []struct{ name, newest, answer string }{
		{"the newest is this release", "1.2.3", "1.2.3"},
		{"the newest is another release", "1.3.0", "1.3.0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFollowRun(t, "latest\n", "1.2.3", test.newest)
			f.mustStart(t)
			if got := strings.TrimSpace(f.answered(t)); got != test.answer {
				t.Errorf("the answer holds %q, want %s", got, test.answer)
			}
		})
	}
}

// spec: installer.md#answering-a-follow-run — a target naming another release is answered
// with that version, and nothing is written but the answer.
func TestAFollowRunAnswersWithTheVersionItsTargetNames(t *testing.T) {
	f := newFollowRun(t, "1.0.0\n", "1.2.3", "1.2.3")
	f.mustStart(t)
	if got := strings.TrimSpace(f.answered(t)); got != "1.0.0" {
		t.Errorf("the answer holds %q, want 1.0.0", got)
	}
	if files := sortedKeys(tree(t, f.destDir)); len(files) != 1 {
		t.Errorf("an answering run wrote files: %v", files)
	}
}

// spec: installer.md#answering-a-follow-run — a panel already at the release is left alone.
// spec: installer.md#staged-installs — the target and the binary in place are read from under
// DESTDIR.
func TestAFollowRunLeavesAnUnchangedPanelAlone(t *testing.T) {
	f := newFollowRun(t, "1.2.3\n", "1.2.3", "1.2.3")
	f.installed(t)
	example := filepath.Join(f.destDir, "etc/prismotr/prismotr.yaml.example")
	if err := os.WriteFile(example, []byte("edited by hand\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout := f.mustStart(t)

	if !strings.Contains(stdout, "already") {
		t.Errorf("the run does not say the panel is already at that version:\n%s", stdout)
	}
	if body, _ := os.ReadFile(example); string(body) != "edited by hand\n" {
		t.Errorf("an unchanged panel was installed over: the example now holds %q", body)
	}
	if got := f.answered(t); got != "" {
		t.Errorf("the answer holds %q; nothing further is needed", got)
	}
}

// spec: installer.md#answering-a-follow-run — a binary in place that is the release's but a
// service definition that differs keeps that binary and installs the rest around it.
func TestAFollowRunKeepsTheReleasesBinaryInPlace(t *testing.T) {
	f := newFollowRun(t, "1.2.3\n", "1.2.3", "1.2.3")
	f.installed(t)
	path := filepath.Join(f.destDir, "usr/local/bin/prismotr")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	unitFile := filepath.Join(f.destDir, "etc/systemd/system/prismotr.service")
	if err := os.WriteFile(unitFile, []byte("# an older unit\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout := f.mustStart(t)

	if got := f.answered(t); got != "" {
		t.Errorf("the answer holds %q; a binary in place that is the release's needs no download", got)
	}
	if !strings.Contains(stdout, "keeping the binary in place") {
		t.Errorf("the run does not say it keeps the binary in place:\n%s", stdout)
	}
	if strings.Contains(stdout, "/usr/local/bin/prismotr") {
		t.Errorf("the run names the binary among the paths it wrote:\n%s", stdout)
	}
	assertLayout(t, f.destDir, "etc/prismotr/target")
	if body, _ := os.ReadFile(unitFile); string(body) == "# an older unit\n" {
		t.Error("the service definition was not installed")
	}
	after, _ := os.Stat(path)
	if !os.SameFile(before, after) {
		t.Error("the binary in place was replaced")
	}
}

// spec: installer.md#answering-a-follow-run — a binary in place that is not the release's asks
// for its binary rather than being kept, and the binary then finishes the panel.
func TestAFollowRunAsksAgainForAnUnfinishedPanel(t *testing.T) {
	for _, test := range []struct {
		name  string
		spoil func(t *testing.T, destDir string)
	}{
		{"a binary its group may write to", func(t *testing.T, destDir string) {
			chmod(t, filepath.Join(destDir, "usr/local/bin/prismotr"), 0o775)
		}},
		{"a symlink to the release's binary", func(t *testing.T, destDir string) {
			path := filepath.Join(destDir, "usr/local/bin/prismotr")
			elsewhere := filepath.Join(t.TempDir(), "prismotr")
			if err := os.Rename(path, elsewhere); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(elsewhere, path); err != nil {
				t.Fatal(err)
			}
		}},
		{"another binary", func(t *testing.T, destDir string) {
			path := filepath.Join(destDir, "usr/local/bin/prismotr")
			if err := os.WriteFile(path, []byte("#!/bin/sh\necho prismotr 1.2.2\n"), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFollowRun(t, "1.2.3\n", "1.2.3", "1.2.3")
			f.installed(t)
			test.spoil(t, f.destDir)

			stdout := f.mustStart(t)
			if strings.Contains(stdout, "already") {
				t.Errorf("the run took an unfinished panel for up to date:\n%s", stdout)
			}
			if got := strings.TrimSpace(f.answered(t)); got != "1.2.3" {
				t.Fatalf("the answer holds %q, want 1.2.3", got)
			}

			if err := os.WriteFile(f.answer, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			f.mustStart(t, f.withBinary()...)
			assertLayout(t, f.destDir, "etc/prismotr/target")
			if got := fileDigest(t, filepath.Join(f.destDir, "usr/local/bin/prismotr")); got != f.digest {
				t.Error("the binary handed over is not the one in place")
			}
		})
	}
}

// spec: installer.md#answering-a-follow-run — every way a follow hand-over is refused, none of
// which installs or answers anything.
func TestAFollowRunRefuses(t *testing.T) {
	without := func(option string) func(f followRun) []string {
		return func(f followRun) []string {
			args := f.args()
			for i, arg := range args {
				if arg != option {
					continue
				}
				if option == "--follow-target" {
					return append(args[:i:i], args[i+1:]...)
				}
				return append(args[:i:i], args[i+2:]...)
			}
			return args
		}
	}
	with := func(option, value string) func(f followRun) []string {
		return func(f followRun) []string {
			args := f.args()
			for i, arg := range args {
				if arg == option {
					args[i+1] = value
				}
			}
			return args
		}
	}
	tests := []struct {
		name    string
		target  string
		prepare func(t *testing.T, f *followRun)
		args    func(f followRun) []string
		names   string
	}{
		{name: "no target file", names: "/etc/prismotr/target"},
		{name: "an empty target", target: "\n", names: "target"},
		{name: "a target with a second line", target: "latest\n\n", names: "target"},
		{name: "a target padded with a blank", target: "latest \n", names: "target"},
		{name: "a target that is not a version", target: "1.2\n", names: "target"},
		{name: "a target that is neither", target: "stable\n", names: "target"},
		{name: "a target that is a symlink", target: "latest\n", prepare: func(t *testing.T, f *followRun) {
			path := filepath.Join(f.destDir, "etc/prismotr/target")
			elsewhere := filepath.Join(t.TempDir(), "target")
			if err := os.WriteFile(elsewhere, []byte("latest\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(elsewhere, path); err != nil {
				t.Fatal(err)
			}
		}, names: "target"},
		{name: "a target writable by its group", target: "latest\n", prepare: func(t *testing.T, f *followRun) {
			chmod(t, filepath.Join(f.destDir, "etc/prismotr/target"), 0o664)
		}, names: "target"},
		{name: "a target writable by anyone", target: "latest\n", prepare: func(t *testing.T, f *followRun) {
			chmod(t, filepath.Join(f.destDir, "etc/prismotr/target"), 0o646)
		}, names: "target"},
		{name: "a target in a directory anyone may write to", target: "latest\n", prepare: func(t *testing.T, f *followRun) {
			chmod(t, filepath.Join(f.destDir, "etc/prismotr"), 0o777)
		}, names: "/etc/prismotr"},
		{name: "no --follow-target", target: "latest\n", args: without("--follow-target"), names: "--follow-target"},
		{name: "no --release", target: "latest\n", args: without("--release"), names: "--release"},
		{name: "no --newest", target: "latest\n", args: without("--newest"), names: "--newest"},
		{name: "no --digest", target: "latest\n", args: without("--digest"), names: "needs --digest"},
		{name: "no --answer", target: "latest\n", args: without("--answer"), names: "--answer"},
		{name: "a --binary naming no file", target: "1.2.3\n", args: func(f followRun) []string {
			return append(f.args(), "--binary", filepath.Join(f.destDir, "no-such-binary"))
		}, names: "no file"},
		{name: "a --release that is not a version", target: "latest\n", args: with("--release", "v1.2.3"), names: "v1.2.3"},
		{name: "a --newest that is not a version", target: "latest\n", args: with("--newest", "v1.3.0"), names: "v1.3.0"},
		{name: "a --digest that is too short", target: "latest\n", args: with("--digest", "abc123"), names: "abc123"},
		{name: "a --digest in capitals", target: "latest\n", args: with("--digest", strings.Repeat("A", 64)), names: strings.Repeat("A", 64)},
		{name: "an --answer naming no file", target: "latest\n", args: func(f followRun) []string {
			return with("--answer", f.answer+".missing")(f)
		}, names: "--answer"},
		{name: "an --answer that is not empty", target: "latest\n", prepare: func(t *testing.T, f *followRun) {
			if err := os.WriteFile(f.answer, []byte("1.0.0\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, names: "--answer"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newFollowRun(t, test.target, "1.2.3", "1.3.0")
			if test.prepare != nil {
				test.prepare(t, &f)
			}
			before := sortedKeys(tree(t, f.destDir))
			answerBefore := f.answered(t)
			var args []string
			if test.args != nil {
				args = test.args(f)
			}

			stdout, stderr, err := f.start(t, args...)

			if err == nil {
				t.Fatalf("the run succeeded; it had to refuse\n%s%s", stdout, stderr)
			}
			if !strings.Contains(stderr, test.names) {
				t.Errorf("the refusal does not name %q:\n%s", test.names, stderr)
			}
			if after := sortedKeys(tree(t, f.destDir)); strings.Join(after, " ") != strings.Join(before, " ") {
				t.Errorf("a refusal wrote files: %v", after)
			}
			if got := f.answered(t); got != answerBefore {
				t.Errorf("a refusal answered %q", got)
			}
		})
	}
}

// spec: installer.md#answering-a-follow-run — a target whose directory is a symlink is refused
// like one that is a symlink itself.
func TestAFollowRunRefusesATargetInASymlinkedDirectory(t *testing.T) {
	f := newFollowRun(t, "latest\n", "1.2.3", "1.3.0")
	configDir := filepath.Join(f.destDir, "etc", "prismotr")
	moved := filepath.Join(t.TempDir(), "prismotr")
	if err := os.Rename(configDir, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, configDir); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := f.start(t)

	if err == nil {
		t.Fatalf("the run succeeded; it had to refuse\n%s%s", stdout, stderr)
	}
	if !strings.Contains(stderr, "symlink") || !strings.Contains(stderr, "etc/prismotr") {
		t.Errorf("the refusal does not name the symlinked directory:\n%s", stderr)
	}
	if got := f.answered(t); got != "" {
		t.Errorf("a refusal answered %q", got)
	}
	if entries, _ := os.ReadDir(moved); len(entries) != 1 {
		t.Errorf("a refusal wrote beside the target: %v", entries)
	}
}

// spec: installer.md#answering-a-follow-run — the installer and the kept script judge a
// version by one grammar; they cannot share a file, so the copies are held equal.
func TestBothScriptsJudgeAVersionAlike(t *testing.T) {
	function := func(file string) string {
		body := read(t, file)
		start := strings.Index(body, "\nis_version() {\n")
		if start < 0 {
			t.Fatalf("%s defines no is_version", file)
		}
		end := strings.Index(body[start:], "\n}\n")
		return body[start : start+end]
	}
	if function(bootstrap) != function(installer) {
		t.Errorf("is_version differs between %s and %s", bootstrap, installer)
	}
}
