package policy

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// goldenSection is the body of the named package's section of the exported-API
// golden, as lines.
func goldenSection(t *testing.T, pkg string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "internal", "policy", "testdata", "exported-api.golden"))
	if err != nil {
		t.Fatal(err)
	}
	for _, section := range strings.Split(string(raw), "== ") {
		if name, body, ok := strings.Cut(section, "\n"); ok && name == pkg {
			return strings.Split(body, "\n")
		}
	}
	t.Fatalf("the golden has no section for %q", pkg)
	return nil
}

// containsInLine reports whether some line of lines starts with prefix and
// contains each of the substrings.
func containsInLine(lines []string, prefix string, substrings ...string) bool {
	return slices.ContainsFunc(lines, func(l string) bool {
		if !strings.HasPrefix(l, prefix) {
			return false
		}
		for _, s := range substrings {
			if !strings.Contains(l, s) {
				return false
			}
		}
		return true
	})
}

// TS-29-43: the exported-API golden records the fourteen-field Config, Resolve,
// Resolution and Agent.Resolution in the root section, Usage.Unpriced and
// UsageJSON's unpriced field in the core section, and the code still matches it.
// 29-REQ-9.1.
func TestGoldenRecordsProviderSelection_TS29_43(t *testing.T) {
	root := goldenSection(t, ".")
	for _, line := range []string{
		"type Config struct { Client core.ProviderClient Provider string Model string Effort Effort Env map[string]string " +
			"Getenv func(string) string System string Prefix []core.Message Toolset Toolset MaxTurns int " +
			"MaxCostUSD float64 Timeout time.Duration Prune PruneOptions MaxTokens int }",
		"func Resolve(cfg Config) (Resolution, error)",
		"type Resolution struct { Provider string Tier catalog.Tier Model core.Model Effort Effort Listed bool Client core.ProviderClient }",
		"func (a *Agent) Resolution() Resolution",
	} {
		if !slices.Contains(root, line) {
			t.Errorf("the root section of the golden lacks %q", line)
		}
	}
	core := goldenSection(t, "core")
	if !containsInLine(core, "type Usage struct {", "BilledModel string Unpriced bool Set UsageField }") {
		t.Error("the core section of the golden does not record Usage.Unpriced")
	}
	if !containsInLine(core, "type UsageJSON struct {", "Reported bool Unpriced bool }") {
		t.Error("the core section of the golden does not record UsageJSON.Unpriced")
	}

	// The golden is the current API: the comparison of TS-13-67 passes.
	cmd := exec.Command("go", "test", "-count=1", "-run", "_TS13_67$", ".")
	cmd.Dir = filepath.Join(repoRoot(t), "internal", "policy")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("the golden comparison fails: %v\n%s", err, out)
	}
}

// TS-29-44: the golden's Config line keeps the tail TS-21-55 checks. 29-REQ-9.2.
func TestGoldenConfigLineKeepsItsTail_TS29_44(t *testing.T) {
	const tail = "Prefix []core.Message Toolset Toolset MaxTurns int MaxCostUSD float64 Timeout time.Duration Prune PruneOptions MaxTokens int }"
	if !containsInLine(goldenSection(t, "."), "type Config struct {", tail) {
		t.Errorf("the Config line of the golden does not end in %q", tail)
	}
	cmd := exec.Command("go", "test", "-count=1", "-run", "_TS21_55$", ".")
	cmd.Dir = filepath.Join(repoRoot(t), "internal", "policy")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("TS-21-55 fails on the golden: %v\n%s", err, out)
	}
}
