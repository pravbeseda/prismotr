package deploy_test

import (
	"os/exec"
	"strings"
	"testing"
)

const tagVersion = "./tag-version.sh"

// spec: release.md#tagging-a-merge — the tag grammar is vMAJOR.MINOR.PATCH and nothing else.
func TestATagNamesAVersionOrIsRefused(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "a release tag", args: []string{"v1.2.3"}, want: "1.2.3"},
		{name: "the first version", args: []string{"v0.0.0"}, want: "0.0.0"},
		{name: "two-digit parts", args: []string{"v10.20.30"}, want: "10.20.30"},
		{name: "a prerelease", args: []string{"v1.2.3-rc1"}},
		{name: "two parts only", args: []string{"v1.2"}},
		{name: "four parts", args: []string{"v1.2.3.4"}},
		{name: "no leading v", args: []string{"1.2.3"}},
		{name: "a placeholder", args: []string{"vX.Y.Z"}},
		{name: "a part that is not a number", args: []string{"v1.a.3"}},
		{name: "a leading zero", args: []string{"v01.2.3"}},
		{name: "a leading zero in the patch", args: []string{"v1.2.03"}},
		{name: "a component too wide to compare", args: []string{"v9999999999.0.0"}},
		{name: "an empty tag", args: []string{""}},
		{name: "no tag at all", args: nil},
		{name: "two tags", args: []string{"v1.2.3", "v1.2.4"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command("sh", append([]string{tagVersion}, test.args...)...)
			var out, errs strings.Builder
			command.Stdout = &out
			command.Stderr = &errs
			err := command.Run()

			if test.want == "" {
				if err == nil {
					t.Fatalf("the run succeeded; the tag had to be refused\n%s", out.String())
				}
				if len(test.args) == 1 && test.args[0] != "" && !strings.Contains(errs.String(), test.args[0]) {
					t.Errorf("the refusal does not name the tag:\n%s", errs.String())
				}
				return
			}
			if err != nil {
				t.Fatalf("the run failed: %v\n%s", err, errs.String())
			}
			if got := strings.TrimSpace(out.String()); got != test.want {
				t.Errorf("printed %q, want %q", got, test.want)
			}
		})
	}
}
