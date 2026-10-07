package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TS-04-58: The root module stays standard-library-only and cgo-free.
// go.mod has no require line, and the internal/policy tests pass.
// (make check is verified externally, not from inside a test.)
func TestGoModHasNoRequire_TS_04_58(t *testing.T) {
	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("reading go.mod: %v", err)
	}
	content := string(data)
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "require") {
			t.Fatalf("go.mod has a require line: %s", trimmed)
		}
	}
}
