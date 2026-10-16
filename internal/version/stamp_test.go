package version_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A release stamps the version with -ldflags -X, which the linker applies to a variable and
// silently ignores on a constant. The real command is built, so this covers the whole path a
// release run takes.
func TestAStampedBuildReportsTheStampedVersion(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "prismotr")
	build := exec.Command("go", "build",
		"-ldflags", "-X github.com/pravbeseda/prismotr/internal/version.Current=9.9.9",
		"-o", binary, "../../cmd/prismotr")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	out, err := exec.Command(binary, "--version").Output()
	if err != nil {
		t.Fatalf("running the stamped binary: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "prismotr 9.9.9" {
		t.Errorf("stamped binary reported %q, want %q", got, "prismotr 9.9.9")
	}
}
