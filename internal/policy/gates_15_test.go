package policy

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TS-15-41: the cross-target and host-cgo gates keep their environment (no
// new variable such as CGO_LDFLAGS_ALLOW) and TS-12-14's budget is still
// 20,000 lines; the gates themselves run in make test. 15-REQ-8.3.
func TestGatesUnchanged_TS15_41(t *testing.T) {
	root := repoRoot(t)
	src, err := os.ReadFile(filepath.Join(root, "internal", "policy", "crosstarget_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	var added []string
	for _, m := range regexp.MustCompile(`cmd\.Env = append\(os\.Environ\(\),([^)]*)\)`).FindAllStringSubmatch(string(src), -1) {
		for _, a := range strings.Split(m[1], ",") {
			if a = strings.TrimSpace(a); a != "" {
				added = append(added, a)
			}
		}
	}
	slices.Sort(added)
	want := []string{`"CGO_ENABLED=0"`, `"CGO_ENABLED=1"`, `"GOARCH="+target.goarch`, `"GOOS="+target.goos`}
	if !slices.Equal(added, want) {
		t.Errorf("the gates add %q to the environment, want exactly %q", added, want)
	}
	if strings.Contains(string(src), "CGO_LDFLAGS_ALLOW") {
		t.Error("a gate sets CGO_LDFLAGS_ALLOW")
	}
	size, err := os.ReadFile(filepath.Join(root, "core", "size_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(size), "budgetLines(t, moduleRoot(t)); n > 20000") {
		t.Error("TS-12-14's budget is no longer 20,000 lines")
	}
}
