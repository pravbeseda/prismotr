// This file drives the real prismotr-install.sh against a synthetic release served over
// loopback and signed with a key the test generates. Nothing here reaches GitHub, and every
// run is staged under DESTDIR.
package deploy_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

const bootstrap = "./prismotr-install.sh"

// origin is a synthetic set of releases: the assets a run may ask for, the manifest that
// names them and the signature over it, per version. version is the newest.
type origin struct {
	dir     string
	assets  map[string][]byte // by "<version>/<asset name>", guarded by mu
	version string            // guarded by mu too
	pub     string
	priv    string
	url     string
	mu      sync.Mutex
	asked   []string
}

func (o *origin) set(name string, body []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.assets[o.version+"/"+name] = body
}

func (o *origin) get(name string) []byte {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.assets[o.version+"/"+name]
}

func (o *origin) remove(name string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.assets, o.version+"/"+name)
}

// tamper replaces one asset of any release, leaving its manifest and signature as they were.
func (o *origin) tamper(version, name string, body []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.assets[version+"/"+name] = body
}

func (o *origin) setNewest(version string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.version = version
}

// publish adds an older release beside the newest: a binary reporting that version, the given
// installer archive, and a manifest signed with the origin's key.
func (o *origin) publish(t *testing.T, version string, archive []byte) {
	t.Helper()
	o.mu.Lock()
	o.assets[version+"/"+binaryName(version)] = syntheticBinary(version)
	if archive != nil {
		o.assets[version+"/prismotr-installer-"+version+".tar.gz"] = archive
	}
	o.mu.Unlock()
	o.signRelease(t, version)
}

func (o *origin) requests() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.asked...)
}

func newOrigin(t *testing.T) *origin {
	t.Helper()
	dir := t.TempDir()
	o := &origin{dir: dir, assets: map[string][]byte{}, version: "1.2.3"}
	o.priv, o.pub = keyPair(t, dir, "origin")
	o.set(o.binaryName(), syntheticBinary(o.version))
	o.set("prismotr-installer-"+o.version+".tar.gz", installerArchive(t, nil))
	o.sign(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o.mu.Lock()
		o.asked = append(o.asked, r.URL.Path)
		newest := o.version
		o.mu.Unlock()
		switch {
		case r.URL.Path == "/releases/latest":
			http.Redirect(w, r, "/releases/tag/v"+newest, http.StatusFound)
		case strings.HasPrefix(r.URL.Path, "/releases/tag/"):
			_, _ = w.Write([]byte("the release page"))
		case strings.HasPrefix(r.URL.Path, "/releases/download/v"):
			name := filepath.Base(r.URL.Path)
			tag := filepath.Base(filepath.Dir(r.URL.Path))
			o.mu.Lock()
			body, ok := o.assets[strings.TrimPrefix(tag, "v")+"/"+name]
			o.mu.Unlock()
			if !ok {
				http.Error(w, "no such asset", http.StatusNotFound)
				return
			}
			_, _ = w.Write(body)
		default:
			http.Error(w, "no", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	o.url = server.URL
	return o
}

func (o *origin) binaryName() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return binaryName(o.version)
}

// syntheticBinary is what a synthetic release publishes: a script that reports its version
// the way the real binary does.
func syntheticBinary(version string) []byte {
	return []byte("#!/bin/sh\necho prismotr " + version + "\n")
}

func binaryName(version string) string {
	return fmt.Sprintf("prismotr-%s-%s", version, hostPlatform())
}

func hostPlatform() string {
	return runtime.GOOS + "-" + runtime.GOARCH
}

func (o *origin) sign(t *testing.T) {
	t.Helper()
	o.mu.Lock()
	version := o.version
	o.mu.Unlock()
	o.signRelease(t, version)
}

func (o *origin) signRelease(t *testing.T, version string) {
	t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	prefix := version + "/"
	manifest := ""
	for key, body := range o.assets {
		name, ok := strings.CutPrefix(key, prefix)
		if !ok || name == "SHA256SUMS" || name == "SHA256SUMS.sig" {
			continue
		}
		sum := sha256.Sum256(body)
		manifest += fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	o.assets[prefix+"SHA256SUMS"] = []byte(manifest)
	path := filepath.Join(o.dir, "SHA256SUMS-"+version)
	write(t, path, manifest)
	openssl(t, "dgst", "-sha256", "-sign", o.priv, "-out", path+".sig", path)
	signature, err := os.ReadFile(path + ".sig")
	if err != nil {
		t.Fatal(err)
	}
	o.assets[prefix+"SHA256SUMS.sig"] = signature
}

// installerArchive is what a release carries: an install.sh that records how it was called
// instead of installing anything. extra entries are added verbatim, which is how a hostile
// archive is built.
func installerArchive(t *testing.T, extra map[string]*tar.Header) []byte {
	t.Helper()
	return packInstaller(t, map[string]string{"install.sh": recordingInstaller}, extra)
}

// recordingInstaller writes what it was handed into $RECORD, which the run inherits.
const recordingInstaller = "#!/bin/sh\nprintf 'args: %s\\n' \"$*\" >> \"$RECORD\"\n" +
	"printf 'binary: %s\\n' \"$(cat \"${2:-/dev/null}\" 2>/dev/null)\" >> \"$RECORD\"\n" +
	"printf 'stdin: [%s]\\n' \"$(cat)\" >> \"$RECORD\"\n" +
	"for sibling in \"$(dirname \"$0\")\"/*; do\n" +
	"  printf 'sibling: %s %s %s\\n' \"$(ls -ln \"$sibling\" | awk '{print $1}')\" " +
	"\"$(ls -ln \"$sibling\" | awk '{print $3}')\" \"$(basename \"$sibling\")\" >> \"$RECORD\"\n" +
	"done\nexit 0\n"

// packInstaller packs files, by name, into the one top-level directory an installer archive
// holds, and adds the extra headers verbatim.
func packInstaller(t *testing.T, files map[string]string, extra map[string]*tar.Header) []byte {
	t.Helper()
	var buffer bytes.Buffer
	zip := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(zip)
	for name, body := range files {
		header := &tar.Header{Name: "prismotr-installer/" + name, Mode: 0o755, Size: int64(len(body))}
		if err := archive.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	for _, header := range extra {
		if err := archive.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zip.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

// bootstrapRun is one invocation of the real script against a synthetic origin.
type bootstrapRun struct {
	origin  *origin
	destDir string
	record  string
	key     string // PRISMOTR_RELEASE_KEY; the origin's own public half by default
	env     []string
	pathDir string
	args    []string
	stdin   string
}

func (r bootstrapRun) start(t *testing.T) (stdout, stderr string, err error) {
	t.Helper()
	path := os.Getenv("PATH")
	if r.pathDir != "" {
		path = r.pathDir
	}
	key := r.key
	if key == "" {
		key = r.origin.pub
	}
	command := exec.Command(bootstrap, r.args...)
	command.Env = append([]string{
		"PATH=" + path,
		"DESTDIR=" + r.destDir,
		"PRISMOTR_RELEASE_ORIGIN=" + r.origin.url,
		"PRISMOTR_RELEASE_KEY=" + key,
		"RECORD=" + r.record,
		"TMPDIR=" + t.TempDir(),
	}, r.env...)
	command.Stdin = strings.NewReader(r.stdin)
	var out, errs bytes.Buffer
	command.Stdout, command.Stderr = &out, &errs
	err = command.Run()
	return out.String(), errs.String(), err
}

func newBootstrapRun(t *testing.T, o *origin, args ...string) bootstrapRun {
	t.Helper()
	return bootstrapRun{origin: o, destDir: t.TempDir(), record: filepath.Join(t.TempDir(), "record"), args: args}
}

func installBinary(t *testing.T, destDir, body string) {
	t.Helper()
	dir := filepath.Join(destDir, "usr", "local", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prismotr"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// spec: installer.md#fetching-and-checking-a-release — a run installs the newest release's
// asset for this platform and says which version it installed.
func TestABootstrapRunInstallsTheNewestRelease(t *testing.T) {
	o := newOrigin(t)
	run := newBootstrapRun(t, o)

	stdout, stderr, err := run.start(t)
	if err != nil {
		t.Fatalf("the run failed: %v\n%s%s", err, stdout, stderr)
	}

	record, err := os.ReadFile(run.record)
	if err != nil {
		t.Fatalf("the installer was never reached: %v\n%s%s", err, stdout, stderr)
	}
	for _, want := range []string{"args: --binary ", "binary: #!/bin/sh\necho prismotr 1.2.3", "stdin: []"} {
		if !strings.Contains(string(record), want) {
			t.Errorf("the handover does not carry %q:\n%s", want, record)
		}
	}
	if !strings.Contains(stdout, "1.2.3") {
		t.Errorf("the run does not name the version it installed:\n%s", stdout)
	}
	if asked := o.requests(); !strings.Contains(strings.Join(asked, " "), o.binaryName()) {
		t.Errorf("the run asked for no binary of its own platform: %v", asked)
	}
}

// spec: installer.md#fetching-and-checking-a-release — every way a release can fail to be
// what it says it is, and none of them installs anything.
func TestABootstrapRunThatMustInstallNothing(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		prepare func(t *testing.T, o *origin)
		says    string
		quiet   bool
	}{
		{name: "a manifest changed after signing", prepare: func(_ *testing.T, o *origin) {
			o.set("SHA256SUMS", append(o.get("SHA256SUMS"), []byte("deadbeef  extra\n")...))
		}, says: "signature"},
		{name: "a manifest signed by another key", prepare: func(t *testing.T, o *origin) {
			other, _ := keyPair(t, o.dir, "someone-else")
			o.priv = other
			o.sign(t)
		}, says: "signature"},
		{name: "an asset whose digest is not the manifest's", prepare: func(_ *testing.T, o *origin) {
			o.set(o.binaryName(), []byte("#!/bin/sh\necho tampered\n"))
		}, says: "digest"},
		{name: "a release whose manifest lists no installer archive", prepare: func(t *testing.T, o *origin) {
			o.remove("prismotr-installer-" + o.version + ".tar.gz")
			o.sign(t)
		}, says: "predates the installer"},
		{name: "a release with no binary for this platform", prepare: func(t *testing.T, o *origin) {
			o.remove(o.binaryName())
			o.sign(t)
		}, says: "lists no prismotr-1.2.3"},
		{name: "a signature file that is empty", prepare: func(_ *testing.T, o *origin) {
			o.set("SHA256SUMS.sig", []byte{})
		}, says: "signature"},
		{name: "a signature file that is not a signature", prepare: func(_ *testing.T, o *origin) {
			o.set("SHA256SUMS.sig", []byte("not a signature at all\n"))
		}, says: "signature"},
		{name: "a version that is not MAJOR.MINOR.PATCH", args: []string{"--version", "1.2"}, says: "not a version", quiet: true},
		{name: "a version no release carries", args: []string{"--version", "9.9.9"}, says: "9.9.9"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			o := newOrigin(t)
			if test.prepare != nil {
				test.prepare(t, o)
			}
			run := newBootstrapRun(t, o, test.args...)

			stdout, stderr, err := run.start(t)

			if err == nil {
				t.Fatalf("the run succeeded; it had to refuse\n%s%s", stdout, stderr)
			}
			if !strings.Contains(stderr, test.says) {
				t.Errorf("the refusal does not say %q:\n%s", test.says, stderr)
			}
			if _, err := os.Stat(run.record); err == nil {
				t.Error("a refusal still handed over to an installer")
			}
			if asked := o.requests(); test.quiet && len(asked) != 0 {
				t.Errorf("the run fetched %v; this row fetches nothing", asked)
			}
		})
	}
}

// spec: installer.md#unpacking — an archive that reaches outside its own directory is not
// unpacked, however good its signature is.
func TestAHostileArchiveIsNotUnpacked(t *testing.T) {
	tests := []struct {
		name  string
		entry *tar.Header
		says  string
	}{
		{"an absolute path", &tar.Header{Name: "/etc/passwd", Mode: 0o644, Size: 0}, "reaches outside itself"},
		{"a path that climbs out", &tar.Header{Name: "prismotr-installer/../../evil", Mode: 0o644, Size: 0}, "reaches outside itself"},
		{"a symlink", &tar.Header{Name: "prismotr-installer/link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}, "not a file or a directory"},
		{"a second top-level entry", &tar.Header{Name: "elsewhere/install.sh", Mode: 0o755, Size: 0}, "not one directory of files"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			o := newOrigin(t)
			o.set("prismotr-installer-"+o.version+".tar.gz", installerArchive(t, map[string]*tar.Header{"x": test.entry}))
			o.sign(t)
			run := newBootstrapRun(t, o)

			stdout, stderr, err := run.start(t)

			if err == nil {
				t.Fatalf("the run succeeded; the archive had to stop it\n%s%s", stdout, stderr)
			}
			if _, err := os.Stat(run.record); err == nil {
				t.Error("a hostile archive still reached an installer")
			}
			if !strings.Contains(stderr, test.says) {
				t.Errorf("the refusal does not say %q:\n%s", test.says, stderr)
			}
		})
	}
}

// spec: installer.md#unpacking — what lands takes no owner and no setuid bit from the archive.
func TestUnpackingStripsSetuid(t *testing.T) {
	o := newOrigin(t)
	archive := installerArchive(t, map[string]*tar.Header{
		"x": {Name: "prismotr-installer/setuid", Mode: 0o4755, Size: 0, Uid: 12345, Gid: 12345},
	})
	o.set("prismotr-installer-"+o.version+".tar.gz", archive)
	o.sign(t)
	run := newBootstrapRun(t, o)

	stdout, stderr, err := run.start(t)
	if err != nil {
		t.Fatalf("the run failed: %v\n%s%s", err, stdout, stderr)
	}

	record, err := os.ReadFile(run.record)
	if err != nil {
		t.Fatalf("the installer was never reached: %v\n%s%s", err, stdout, stderr)
	}
	found := false
	for _, line := range strings.Split(string(record), "\n") {
		fields := strings.Fields(strings.TrimPrefix(line, "sibling: "))
		if !strings.HasPrefix(line, "sibling: ") || len(fields) != 3 || fields[2] != "setuid" {
			continue
		}
		found = true
		if strings.ContainsAny(fields[0], "sS") {
			t.Errorf("the extracted file kept a setuid or setgid bit: %s", fields[0])
		}
		if fields[1] == "12345" {
			t.Errorf("the extracted file kept the archive's owner: %s", fields[1])
		}
	}
	if !found {
		t.Fatalf("the installer saw no file called setuid beside it:\n%s", record)
	}
}

// spec: installer.md#the-handover — the installer is given /dev/null however the run itself
// was invoked.
func TestTheInstallerIsHandedNoStdin(t *testing.T) {
	o := newOrigin(t)
	run := newBootstrapRun(t, o)
	run.stdin = "nothing the installer should read"

	stdout, stderr, err := run.start(t)
	if err != nil {
		t.Fatalf("the run failed: %v\n%s%s", err, stdout, stderr)
	}
	if record, _ := os.ReadFile(run.record); !strings.Contains(string(record), "stdin: []") {
		t.Errorf("the installer was handed something on stdin:\n%s", record)
	}
}

// spec: installer.md#staged-installs — the seams that let this test exist are refused by a
// run that is not staged, so they cannot become a way around the key.
func TestTheSeamsAreRefusedByARunThatIsNotStaged(t *testing.T) {
	for _, seam := range []string{"PRISMOTR_RELEASE_ORIGIN", "PRISMOTR_RELEASE_KEY"} {
		t.Run(seam, func(t *testing.T) {
			o := newOrigin(t)
			run := newBootstrapRun(t, o)
			run.destDir = ""
			run.env = []string{"PRISMOTR_RELEASE_ORIGIN=", "PRISMOTR_RELEASE_KEY=", seam + "=" + o.url}

			stdout, stderr, err := run.start(t)

			if err == nil {
				t.Fatalf("the run succeeded; the seam had to stop it\n%s%s", stdout, stderr)
			}
			if !strings.Contains(stderr, seam) {
				t.Errorf("the refusal does not name %s:\n%s", seam, stderr)
			}
		})
	}
}

// spec: installer.md#fetching-and-checking-a-release — curl, openssl, tar and find are the
// tools the run needs, and a machine without one is told which one it is.
func TestABootstrapRunNamesTheToolItIsMissing(t *testing.T) {
	tools := []string{"curl", "openssl", "tar", "find"}
	for _, missing := range tools {
		t.Run(missing, func(t *testing.T) {
			o := newOrigin(t)
			run := newBootstrapRun(t, o)
			run.pathDir = pathWithout(t, tools, missing)

			stdout, stderr, err := run.start(t)

			if err == nil {
				t.Fatalf("the run succeeded; a PATH without %s had to stop it\n%s%s", missing, stdout, stderr)
			}
			if want := missing + " is not installed, and this needs it"; !strings.Contains(stderr, want) {
				t.Errorf("the refusal does not name %s:\n%s", missing, stderr)
			}
			if asked := o.requests(); len(asked) != 0 {
				t.Errorf("a run without %s still fetched %v", missing, asked)
			}
		})
	}
}

// pathWithout is a single-directory PATH holding every tool but one.
func pathWithout(t *testing.T, tools []string, missing string) string {
	t.Helper()
	dir := t.TempDir()
	for _, tool := range tools {
		if tool == missing {
			continue
		}
		found, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s is not on this machine's PATH: %v", tool, err)
		}
		if err := os.Symlink(found, filepath.Join(dir, tool)); err != nil {
			t.Fatalf("cannot stage %s: %v", tool, err)
		}
	}
	return dir
}

// spec: installer.md#fetching-and-checking-a-release — a release older than what is installed
// is an operator's slip, and the guard says so rather than installing it.
func TestABootstrapRunRefusesToGoBackwards(t *testing.T) {
	o := newOrigin(t)
	run := newBootstrapRun(t, o)
	installBinary(t, run.destDir, "#!/bin/sh\necho prismotr 9.9.9\n")

	stdout, stderr, err := run.start(t)
	if err == nil {
		t.Fatalf("the run succeeded; 1.2.3 is older than 9.9.9\n%s%s", stdout, stderr)
	}
	if !strings.Contains(stderr, "9.9.9") || !strings.Contains(stderr, "--allow-downgrade") {
		t.Errorf("the refusal does not name the installed version and the way past it:\n%s", stderr)
	}

	run.args = append(run.args, "--allow-downgrade")
	if stdout, stderr, err = run.start(t); err != nil {
		t.Fatalf("--allow-downgrade did not install it: %v\n%s%s", err, stdout, stderr)
	}
}

// spec: installer.md#the-two-halves — the key the script carries is the key the repository
// publishes; two copies that could drift are worse than one.
func TestTheCarriedKeyIsTheShippedKey(t *testing.T) {
	script := read(t, bootstrap)
	shippedKey := read(t, "release-signing-key.pub")
	if !strings.Contains(script, strings.TrimSpace(shippedKey)) {
		t.Errorf("%s does not carry the key in release-signing-key.pub", bootstrap)
	}
}

// spec: release.md#the-key — the fingerprint the release spec publishes, and ansible-hosts
// carries, is this key's and not a stale copy of an older one.
func TestTheSpecPublishesThisKeysFingerprint(t *testing.T) {
	der, err := exec.Command("openssl", "pkey", "-pubin", "-in", "release-signing-key.pub", "-outform", "DER").Output()
	if err != nil {
		t.Fatalf("reading the shipped key: %v", err)
	}
	sum := sha256.Sum256(der)
	fingerprint := hex.EncodeToString(sum[:])
	if !strings.Contains(read(t, "../docs/specs/release.md"), fingerprint) {
		t.Errorf("docs/specs/release.md does not publish this key's fingerprint %s", fingerprint)
	}
}

// spec: installer.md#fetching-and-checking-a-release — a version given explicitly is the one
// installed, and the run does not ask the origin what the newest is.
func TestAVersionGivenIsTheVersionInstalled(t *testing.T) {
	o := newOrigin(t)
	run := newBootstrapRun(t, o, "--version", "1.2.3")

	stdout, stderr, err := run.start(t)
	if err != nil {
		t.Fatalf("the run failed: %v\n%s%s", err, stdout, stderr)
	}
	for _, path := range o.requests() {
		if strings.Contains(path, "releases/latest") {
			t.Errorf("the run asked for the newest release although it was given one: %v", o.requests())
		}
	}
}

// spec: installer.md#fetching-and-checking-a-release — 1.10.0 is newer than 1.9.0.
func TestAVersionIsComparedNumerically(t *testing.T) {
	o := newOrigin(t)
	o.setNewest("1.10.0")
	o.set(o.binaryName(), syntheticBinary("1.10.0"))
	o.set("prismotr-installer-1.10.0.tar.gz", installerArchive(t, nil))
	o.sign(t)
	run := newBootstrapRun(t, o)
	installBinary(t, run.destDir, "#!/bin/sh\necho prismotr 1.9.0\n")

	stdout, stderr, err := run.start(t)
	if err != nil {
		t.Fatalf("1.10.0 was refused over an installed 1.9.0: %v\n%s%s", err, stdout, stderr)
	}
}

// spec: installer.md#fetching-and-checking-a-release — an installed binary that cannot say
// which version it is does not block the run, and the run says why it went ahead.
func TestABinaryThatCannotSayItsVersionDoesNotBlockTheRun(t *testing.T) {
	o := newOrigin(t)
	run := newBootstrapRun(t, o)
	for _, body := range []string{
		"#!/bin/sh\nexit 1\n",
		"#!/bin/sh\necho prismotr not-a-version\n",
		"#!/bin/sh\necho prismotr 1.2\n",
		"#!/bin/sh\necho prismotr 999999999999999999999999999.0.0\n",
	} {
		installBinary(t, run.destDir, body)
		stdout, stderr, err := run.start(t)
		if err != nil {
			t.Fatalf("the run failed: %v\n%s%s", err, stdout, stderr)
		}
		if !strings.Contains(stdout, "could not tell") {
			t.Errorf("the run does not say it could not read the installed version:\n%s", stdout)
		}
	}
}

// spec: installer.md#fetching-and-checking-a-release — an installed binary another account
// could have replaced is not run at all.
func TestAReplaceableBinaryIsNotRunToReadItsVersion(t *testing.T) {
	o := newOrigin(t)
	run := newBootstrapRun(t, o)
	installBinary(t, run.destDir, "#!/bin/sh\necho prismotr 9.9.9\n")
	chmod(t, filepath.Join(run.destDir, "usr/local/bin/prismotr"), 0o777)

	stdout, stderr, err := run.start(t)
	if err != nil {
		t.Fatalf("the run failed: %v\n%s%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "another account can replace it") {
		t.Errorf("the run does not say why it did not read the version:\n%s", stdout)
	}
}

// spec: installer.md#fetching-and-checking-a-release — the ways a run can be asked the wrong
// question, none of which fetches anything.
func TestABootstrapRunRefusesItsArguments(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		says  string
		usage bool // an option-shaped mistake gets the usage; a value that is no version does not
	}{
		{"an unknown option", []string{"--wat"}, "--wat", true},
		{"a stray word", []string{"hub"}, "hub", true},
		{"an option without its value", []string{"--version"}, "--version", true},
		{"an empty version", []string{"--version", ""}, "--version", true},
		{"a version with a trailing dot", []string{"--version", "1.2.3."}, "not a version", false},
		{"a version with a doubled dot", []string{"--version", "1..3"}, "not a version", false},
		{"a component no shell compares as a number", []string{"--version", "9999999999.0.0"}, "not a version", false},
		{"--follow-target with --version", []string{"--follow-target", "--version", "1.2.3"}, "not --version", true},
		{"--follow-target with --allow-downgrade", []string{"--follow-target", "--allow-downgrade"}, "only when the target names it", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			o := newOrigin(t)
			run := newBootstrapRun(t, o, test.args...)

			stdout, stderr, err := run.start(t)

			if err == nil {
				t.Fatalf("the run succeeded; it had to refuse\n%s%s", stdout, stderr)
			}
			if !strings.Contains(stderr, test.says) {
				t.Errorf("the refusal does not say %q:\n%s", test.says, stderr)
			}
			if test.usage != strings.Contains(stderr, "usage:") {
				t.Errorf("usage printed = %v, want %v:\n%s", !test.usage, test.usage, stderr)
			}
			if asked := o.requests(); len(asked) != 0 {
				t.Errorf("a refused argument still fetched %v", asked)
			}
		})
	}
}

func TestBootstrapHelpGoesToStdout(t *testing.T) {
	o := newOrigin(t)
	run := newBootstrapRun(t, o, "-h")
	stdout, stderr, err := run.start(t)
	if err != nil {
		t.Fatalf("-h failed: %v\n%s%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "usage:") || stderr != "" {
		t.Errorf("the usage is not on stdout alone:\nstdout: %s\nstderr: %s", stdout, stderr)
	}
}

// spec: installer.md#fetching-and-checking-a-release — an origin that cannot be reached
// leaves the host as it was.
func TestAnOriginThatCannotBeReachedInstallsNothing(t *testing.T) {
	o := newOrigin(t)
	run := newBootstrapRun(t, o)
	run.env = []string{"PRISMOTR_RELEASE_ORIGIN=http://127.0.0.1:1"}

	stdout, stderr, err := run.start(t)

	if err == nil {
		t.Fatalf("the run succeeded; the origin is not there\n%s%s", stdout, stderr)
	}
	if _, err := os.Stat(run.record); err == nil {
		t.Error("an unreachable origin still reached an installer")
	}
}

// spec: installer.md#unpacking — the working directory is the run's own and is gone when the
// run ends, whether it installed anything or refused.
func TestTheWorkingDirectoryIsGoneAfterwards(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{"a run that installs", nil},
		{"a run that refuses", []string{"--version", "9.9.9"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := newOrigin(t)
			run := newBootstrapRun(t, o, test.args...)
			tmp := t.TempDir()
			run.env = []string{"TMPDIR=" + tmp}

			_, _, _ = run.start(t)

			left, err := os.ReadDir(tmp)
			if err != nil {
				t.Fatal(err)
			}
			if len(left) != 0 {
				t.Errorf("the run left %d entries behind in its TMPDIR", len(left))
			}
		})
	}
}

// realArchive packs the installer exactly as the release workflow does, so what the tests
// unpack is what a release carries.
func realArchive(t *testing.T) []byte {
	t.Helper()
	staging := filepath.Join(t.TempDir(), "prismotr-installer")
	if err := os.MkdirAll(filepath.Join(staging, "systemd"), 0o755); err != nil {
		t.Fatal(err)
	}
	copies := map[string]string{
		"install.sh":               "install.sh",
		"prismotr.env.example":     "prismotr.env.example",
		"../prismotr.yaml.example": "prismotr.yaml.example",
	}
	for source, name := range copies {
		body, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(name, ".sh") {
			mode = 0o755
		}
		if err := os.WriteFile(filepath.Join(staging, name), body, mode); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir("systemd")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		body, err := os.ReadFile(filepath.Join("systemd", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(staging, "systemd", entry.Name()), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	packed, err := exec.Command("tar", "-czf", "-", "-C", filepath.Dir(staging), "prismotr-installer").Output()
	if err != nil {
		t.Fatalf("packing the installer: %v", err)
	}
	return packed
}

// spec: installer.md#installing — the whole chain, with the installer a release really
// carries: the host ends in the state the layout describes, the service stopped for want of
// a configuration.
func TestTheWholeChainInstalls(t *testing.T) {
	o := newOrigin(t)
	o.set("prismotr-installer-"+o.version+".tar.gz", realArchive(t))
	o.sign(t)
	run := newBootstrapRun(t, o)

	stdout, stderr, err := run.start(t)
	if err != nil {
		t.Fatalf("the chain failed: %v\n%s%s", err, stdout, stderr)
	}

	assertLayout(t, run.destDir)
	if !strings.Contains(stdout, "prismotr.yaml") {
		t.Errorf("the run does not name the configuration it still needs:\n%s", stdout)
	}
}

// spec: installer.md#staged-installs — a run that is not staged and not root writes nothing,
// and says which it is.
func TestARunThatIsNeitherStagedNorRootRefuses(t *testing.T) {
	o := newOrigin(t)
	run := newBootstrapRun(t, o)
	run.destDir = ""
	run.env = []string{"PRISMOTR_RELEASE_ORIGIN=", "PRISMOTR_RELEASE_KEY="}

	stdout, stderr, err := run.start(t)

	if err == nil {
		t.Fatalf("the run succeeded; the suite does not run as root\n%s%s", stdout, stderr)
	}
	if !strings.Contains(stderr, "must run as root") {
		t.Errorf("the refusal does not name the cause:\n%s", stderr)
	}
	if asked := o.requests(); len(asked) != 0 {
		t.Errorf("a run that could not install still fetched %v", asked)
	}
}
