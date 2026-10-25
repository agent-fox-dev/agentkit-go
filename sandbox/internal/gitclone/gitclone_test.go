package gitclone_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/gitclone"
)

const okURL = "https://github.com/agent-fox-dev/agentkit-go"

func TestCheckRefusesADefectiveURLNamingTheField(t *testing.T) {
	for _, u := range []string{
		"http://h/r", "ssh://h/r", "file:///r", "ext::sh -c x", "git://h/r", "https:///r",
		"https://u:p@h/r", "https://u@h/r", "https://h/r#frag", "https://h/%zz",
		"https://h/r x", "https://h/r\t", "https://h/r\x00", "https://h/r\x7f",
	} {
		err := gitclone.Check(sandbox.GitRepo{URL: u})
		if err == nil || !strings.Contains(err.Error(), "URL") {
			t.Errorf("Check(URL %q) = %v, want an error naming URL", u, err)
		}
	}
}

func TestCheckRefusesADefectiveRefNamingTheField(t *testing.T) {
	for _, r := range []string{
		"-b", "--upload-pack=x", "/main", "a..b", "...", "main~1", "main^", "a:b", "a b", "main\n",
		"main@{1}", "refs*x", "é",
	} {
		err := gitclone.Check(sandbox.GitRepo{URL: okURL, Ref: r})
		if err == nil || !strings.Contains(err.Error(), "Ref") {
			t.Errorf("Check(Ref %q) = %v, want an error naming Ref", r, err)
		}
	}
}

func TestCheckRefusesANegativeDepth(t *testing.T) {
	if err := gitclone.Check(sandbox.GitRepo{URL: okURL, Depth: -1}); err == nil || !strings.Contains(err.Error(), "Depth") {
		t.Errorf("Check(Depth -1) = %v, want an error naming Depth", err)
	}
}

func TestCheckAcceptsAValidRepository(t *testing.T) {
	for _, ref := range []string{"", "main", "v1.2.3", "release/v1.2_x", "feature-x.y"} {
		for _, d := range []int{0, 1} {
			if err := gitclone.Check(sandbox.GitRepo{URL: okURL, Ref: ref, Depth: d}); err != nil {
				t.Errorf("Check(Ref %q, Depth %d) = %v, want nil", ref, d, err)
			}
		}
	}
}

// Args is the https-only protocol prefix and clone, --depth and --branch
// exactly when set, then -- URL dir.
func TestArgsAddsDepthAndBranchOnlyWhenSet(t *testing.T) {
	u, d := okURL, "/workspace"
	fixed := []string{"-c", "protocol.allow=never", "-c", "protocol.https.allow=always", "clone"}
	for _, c := range []struct {
		repo sandbox.GitRepo
		want []string
	}{
		{sandbox.GitRepo{URL: u, Ref: "main", Depth: 1}, append(slices.Clone(fixed), "--depth", "1", "--branch", "main", "--", u, d)},
		{sandbox.GitRepo{URL: u}, append(slices.Clone(fixed), "--", u, d)},
		{sandbox.GitRepo{URL: u, Depth: 3}, append(slices.Clone(fixed), "--depth", "3", "--", u, d)},
		{sandbox.GitRepo{URL: u, Ref: "v1.0"}, append(slices.Clone(fixed), "--branch", "v1.0", "--", u, d)},
		{sandbox.GitRepo{URL: u, Ref: "-x"}, append(slices.Clone(fixed), "--branch", "-x", "--", u, d)},
	} {
		if got := gitclone.Args(c.repo, d); !slices.Equal(got, c.want) {
			t.Errorf("Args(%+v) = %q, want %q", c.repo, got, c.want)
		}
	}
}
