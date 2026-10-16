package version_test

import (
	"regexp"
	"testing"

	"github.com/pravbeseda/prismotr/internal/version"
)

// The development default is a version too, and one that sorts below every release so the
// installer's downgrade guard never mistakes a development build for a newer one.
func TestCurrentIsSemver(t *testing.T) {
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(version.Current) {
		t.Errorf("version.Current = %q, want MAJOR.MINOR.PATCH", version.Current)
	}
}
