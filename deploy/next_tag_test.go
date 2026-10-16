package deploy_test

import (
	"os/exec"
	"strings"
	"testing"
)

const nextTag = "./next-tag.sh"

// spec: release.md#tagging-a-merge — the next tag comes from the highest release tag and the
// merged pull request's release: label, and a label set that says two things is refused.
func TestTheNextTagFollowsTheHighestTagAndTheLabel(t *testing.T) {
	tests := []struct {
		name    string
		tags    string
		labels  []string
		want    string
		refused []string
	}{
		{name: "versions compare as numbers", tags: "v1.10.0\nv1.2.3\n", want: "v1.10.1"},
		{name: "a minor label", tags: "v1.2.3\n", labels: []string{"release:minor"}, want: "v1.3.0"},
		{name: "a major label", tags: "v1.2.3\n", labels: []string{"release:major"}, want: "v2.0.0"},
		{name: "a none label", tags: "v1.2.3\n", labels: []string{"release:none"}},
		{name: "other labels are ignored", tags: "v1.2.3\n", labels: []string{"bug", "release:minor", "needs review"}, want: "v1.3.0"},
		{name: "labels match regardless of case", tags: "v1.2.3\n", labels: []string{"Release:Minor"}, want: "v1.3.0"},
		{name: "a label given twice", tags: "v1.2.3\n", labels: []string{"release:minor", "release:minor"}, want: "v1.3.0"},
		{name: "two different release labels", tags: "v1.2.3\n", labels: []string{"release:minor", "release:major"}, refused: []string{"release:minor", "release:major"}},
		{name: "an unknown release label", tags: "v1.2.3\n", labels: []string{"release:patch"}, refused: []string{"release:patch"}},
		{name: "an empty release label", tags: "v1.2.3\n", labels: []string{"release:"}, refused: []string{"release:"}},
		{name: "no tags, no labels", want: "v0.0.1"},
		{name: "no tags, a minor label", labels: []string{"release:minor"}, want: "v0.1.0"},
		{name: "no tags, a major label", labels: []string{"release:major"}, want: "v1.0.0"},
		{name: "lines that name no version", tags: "v1.2\nlatest\nv1.2.3-rc1\nv01.2.3\nv2.0.0 \n\nv1.0.0\n", want: "v1.0.1"},
		{name: "a patch too wide to be a version", tags: "v1.2.999999999\n", refused: []string{"v1.2.1000000000"}},
		{name: "a major too wide to be a version", tags: "v999999999.0.0\n", labels: []string{"release:major"}, refused: []string{"v1000000000.0.0"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command("sh", append([]string{nextTag}, test.labels...)...)
			command.Stdin = strings.NewReader(test.tags)
			var out, errs strings.Builder
			command.Stdout = &out
			command.Stderr = &errs
			err := command.Run()

			if test.refused != nil {
				if err == nil {
					t.Fatalf("the run succeeded; it had to be refused\n%s", out.String())
				}
				for _, named := range test.refused {
					if !strings.Contains(errs.String(), named) {
						t.Errorf("the refusal does not name %q:\n%s", named, errs.String())
					}
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

// spec: release.md#tagging-a-merge — release:none prints nothing and exits 0 even when the
// tags arrive through a pipe: stopping before the input ends would kill the command feeding it.
func TestReleaseNoneLeavesThePipeFeedingItUnbroken(t *testing.T) {
	feed := `{ head -c 100000 /dev/zero | tr '\0' x; printf '\nv1.2.3\n'; } | sh ` + nextTag + ` release:none`
	command := exec.Command("bash", "-e", "-o", "pipefail", "-c", feed)
	var out, errs strings.Builder
	command.Stdout = &out
	command.Stderr = &errs
	if err := command.Run(); err != nil {
		t.Fatalf("the pipeline failed: %v\n%s", err, errs.String())
	}
	if out.String() != "" {
		t.Errorf("printed %q, want nothing", out.String())
	}
}
