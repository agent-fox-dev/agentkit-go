package gitclone_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/gitclone"
)

const okURL = "https://github.com/agent-fox-dev/agentkit-go"

// TS-17-2: every defective URL is refused with an error naming the URL
// field. 17-REQ-1.2.
func TestCheckRefusesBadURLs_TS17_2(t *testing.T) {
	for _, u := range []string{
		"http://h/r", "ssh://h/r", "file:///r", "ext::sh -c x", "git://h/r", "https:///r",
		"https://u:p@h/r", "https://u@h/r", "https://h/r#frag",
		"https://h/r x", "https://h/r\t", "https://h/r\x00", "https://h/r\x7f",
	} {
		err := gitclone.Check(sandbox.GitRepo{URL: u})
		if err == nil || !strings.Contains(err.Error(), "URL") {
			t.Errorf("Check(URL %q) = %v, want an error naming URL", u, err)
		}
	}
}

// TS-17-3: every defective Ref is refused with an error naming the Ref
// field. 17-REQ-1.3.
func TestCheckRefusesBadRefs_TS17_3(t *testing.T) {
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

// TS-17-4: a negative Depth is refused, and every valid repository is
// accepted. 17-REQ-1.4.
func TestCheckDepthAndValid_TS17_4(t *testing.T) {
	if err := gitclone.Check(sandbox.GitRepo{URL: okURL, Depth: -1}); err == nil || !strings.Contains(err.Error(), "Depth") {
		t.Errorf("Check(Depth -1) = %v, want an error naming Depth", err)
	}
	for _, ref := range []string{"", "main", "v1.2.3", "release/v1.2_x", "feature-x.y"} {
		for _, d := range []int{0, 1} {
			if err := gitclone.Check(sandbox.GitRepo{URL: okURL, Ref: ref, Depth: d}); err != nil {
				t.Errorf("Check(Ref %q, Depth %d) = %v, want nil", ref, d, err)
			}
		}
	}
}

// TS-17-5: Args returns the fixed clone argv, with --depth and --branch only
// when set. 17-REQ-1.5.
func TestArgs_TS17_5(t *testing.T) {
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
	} {
		if got := gitclone.Args(c.repo, d); !slices.Equal(got, c.want) {
			t.Errorf("Args(%+v) = %q, want %q", c.repo, got, c.want)
		}
	}
}
