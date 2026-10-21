package catalog

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// TS-26-28: a returned Model is a deep copy: changing its Efforts values,
// keys or Compat bytes does not change what a later identical call returns.
func TestSelectReturnsDeepCopies_TS26_28(t *testing.T) {
	for _, tc := range [][3]string{
		{"openrouter", "STANDARD", ""},
		{"anthropic", "claude-sonnet-5-5", ""},
		{"anthropic", "ADVANCED", ""},
		{"anthropic", "claude-fable-5-1", ""}, // a row with a Compat object
		{"ollama", "qwen3.8:27b", "high"},
		{"openrouter", "vendor/unlisted", ""},
	} {
		first, err := Select(tc[0], tc[1], core.Effort(tc[2]))
		if err != nil {
			t.Fatalf("Select%q: %v", tc, err)
		}
		pristine, err := json.Marshal(first)
		if err != nil {
			t.Fatal(err)
		}

		for k, w := range first.Model.Efforts {
			if w != nil {
				*w = "mutated"
			}
			first.Model.Efforts[k] = nil
		}
		delete(first.Model.Efforts, core.EffortLow)
		if len(first.Model.Compat) > 0 {
			first.Model.Compat[0] ^= 0xff
		}

		again, err := Select(tc[0], tc[1], core.Effort(tc[2]))
		if err != nil {
			t.Fatalf("Select%q: %v", tc, err)
		}
		got, err := json.Marshal(again)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(pristine) {
			t.Errorf("Select%q changed after its first result was mutated:\n got %s\nwant %s", tc, got, pristine)
		}
	}
}

// TS-26-29: Select is safe for concurrent use; run it under go test -race.
func TestSelectIsSafeForConcurrentUse_TS26_29(t *testing.T) {
	triples := [][3]string{
		{"anthropic", "STANDARD", ""},
		{"openrouter", "advanced", "low"},
		{"ollama", "SIMPLE", ""},
		{"anthropic", "claude-opus-5-5", "xhigh"},
		{"openrouter", "qwen/qwen3.8-flash", ""},
		{"ollama", "llama4:8b", "HIGH"},
		{"", "claude-future-9", ""},
		{"openai", "x", ""},
		{"ollama", "", ""},
		{"anthropic", "x", "turbo"},
	}
	type result struct {
		sel Selection
		err error
	}
	want := make([]result, len(triples))
	for i, tr := range triples {
		want[i].sel, want[i].err = Select(tr[0], tr[1], core.Effort(tr[2]))
	}

	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i, tr := range triples {
				sel, err := Select(tr[0], tr[1], core.Effort(tr[2]))
				if !reflect.DeepEqual(sel, want[i].sel) || !reflect.DeepEqual(err, want[i].err) {
					t.Errorf("Select%q = %+v, %v; sequentially %+v, %v", tr, sel, err, want[i].sel, want[i].err)
				}
				for k := range sel.Model.Efforts { // each goroutine owns its result
					sel.Model.Efforts[k] = nil
				}
				if len(sel.Model.Compat) > 0 {
					sel.Model.Compat[0] ^= 0xff
				}
			}
		}()
	}
	wg.Wait()
}

// nonTestImports returns the import paths of each non-test Go file of this
// package, by file name.
func nonTestImports(t *testing.T) map[string][]string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range parsed.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			out[f] = append(out[f], path)
		}
	}
	if len(out) == 0 {
		t.Fatal("found no non-test Go files in the package directory")
	}
	return out
}

// TS-26-30: Select ignores the environment, and no non-test file of the package
// imports os, net or net/http.
func TestSelectIgnoresTheEnvironment_TS26_30(t *testing.T) {
	triples := [][3]string{
		{"openrouter", "STANDARD", ""},
		{"ollama", "qwen3.8:27b", "high"},
		{"anthropic", "claude-future-9", ""},
		{"ollama", "llama4:8b", ""},
	}
	results := func() []Selection {
		var out []Selection
		for _, tr := range triples {
			sel, err := Select(tr[0], tr[1], core.Effort(tr[2]))
			if err != nil {
				t.Fatalf("Select%q: %v", tr, err)
			}
			out = append(out, sel)
		}
		return out
	}

	keys := []string{"OPENROUTER_API_KEY", "OLLAMA_HOST", "ANTHROPIC_API_KEY"}
	for _, k := range keys {
		t.Setenv(k, "") // registers the restore; the variable is then removed
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
	unset := results()

	t.Setenv("OPENROUTER_API_KEY", "k")
	t.Setenv("OLLAMA_HOST", "http://x:1")
	t.Setenv("ANTHROPIC_API_KEY", "k")
	if set := results(); !reflect.DeepEqual(unset, set) {
		t.Errorf("results differ with the variables set:\n unset %+v\n   set %+v", unset, set)
	}

	for file, imports := range nonTestImports(t) {
		for _, banned := range []string{"os", "net", "net/http"} {
			if slices.Contains(imports, banned) {
				t.Errorf("%s imports %s", file, banned)
			}
		}
	}
}

// goList runs go list with args in the package directory and returns its
// output, one line per element.
func goList(t *testing.T, args ...string) []string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go is not on PATH: %v", err)
	}
	out, err := exec.Command("go", append([]string{"list"}, args...)...).Output()
	if err != nil {
		t.Fatalf("go list %v: %v", args, err)
	}
	return strings.Fields(string(out))
}

// TS-26-34: the package imports only core and the standard library, and has no
// resolve.go or clamp.go.
//
// "Only core" is read as the package's own imports: go list -deps also names
// the packages core imports (schema), which the package reaches through core
// and does not import itself.
func TestPackageImportsOnlyCore_TS26_34(t *testing.T) {
	module := goList(t, "-m")[0]
	corePkg := module + "/core"

	for file, imports := range nonTestImports(t) {
		for _, imp := range imports {
			if first, _, _ := strings.Cut(imp, "/"); strings.Contains(first, ".") && imp != corePkg {
				t.Errorf("%s imports %s; only %s and the standard library are allowed", file, imp, corePkg)
			}
		}
	}

	const nonStd = "{{if not .Standard}}{{.ImportPath}}{{end}}"
	allowed := append(goList(t, "-deps", "-f", nonStd, corePkg), module+"/catalog")
	for _, dep := range goList(t, "-deps", "-f", nonStd, ".") {
		if !slices.Contains(allowed, dep) {
			t.Errorf("the package depends on %s, which core does not bring in", dep)
		}
	}

	for _, gone := range []string{"resolve.go", "clamp.go"} {
		if _, err := os.Stat(gone); err == nil {
			t.Errorf("catalog/%s exists", gone)
		}
	}
}
