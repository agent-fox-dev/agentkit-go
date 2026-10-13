package images

import (
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TS-16-15: .dockerignore excludes every listed path and nothing go.mod,
// go.sum or a package of the root or sandbox module needs (the agent is
// built in the nested sandbox module, which replaces the root module with
// ../). 16-REQ-1.7.
func TestDockerignore_TS16_15(t *testing.T) {
	root := repoRoot(t)
	var patterns []string
	for _, l := range strings.Split(readRepo(t, ".dockerignore"), "\n") {
		l = strings.Trim(strings.TrimSpace(l), "/")
		if l != "" && !strings.HasPrefix(l, "#") {
			patterns = append(patterns, l)
		}
	}
	for _, want := range []string{".git", ".github", ".specs", ".claude", "build", "codesearch", "docs", "coverage.*", "*.test", ".DS_Store"} {
		if !slices.Contains(patterns, want) {
			t.Errorf(".dockerignore lacks %s", want)
		}
	}
	matches := func(rel string) string {
		for _, p := range patterns {
			if ok, _ := filepath.Match(p, rel); ok {
				return p
			}
		}
		return ""
	}
	for _, f := range []string{"go.mod", "go.sum", "sandbox/go.mod", "sandbox/go.sum"} {
		if p := matches(f); p != "" {
			t.Errorf("%s matches %s", f, p)
		}
	}
	dirs := map[string]bool{}
	for _, mod := range []string{root, filepath.Join(root, "sandbox")} {
		cmd := exec.Command("go", "list", "-f", "{{.Dir}}", "./...")
		cmd.Dir = mod
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("go list in %s: %v", mod, err)
		}
		for _, d := range strings.Fields(string(out)) {
			rel, _ := filepath.Rel(root, d)
			for r := filepath.ToSlash(rel); r != "." && r != ""; r = filepath.ToSlash(filepath.Dir(r)) {
				dirs[r] = true
			}
		}
	}
	for _, need := range []string{"sandbox/images", "sandbox/cmd/agentkit-sandboxd"} {
		dirs[need] = true
	}
	for d := range dirs {
		if p := matches(d); p != "" {
			t.Errorf("the package directory %s matches %s", d, p)
		}
	}
}
