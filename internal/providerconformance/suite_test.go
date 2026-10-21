package providerconformance

import (
	"bufio"
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// childEnv marks a go test run that a test of this package started, so that it
// does not start another.
const childEnv = "AGENTKIT_CONFORMANCE_CHILD"

// specProviders and specCases are the providers and the cases the spec names,
// written down here so that the table the suite runs cannot change them.
var (
	specProviders = []string{"anthropic", "openrouter", "ollama"}
	specCases     = []string{"text", "tool_call", "tool_call_stop", "thinking", "cut_off", "error_status", "error_in_stream"}
)

// testEvent is a line of go test -json.
type testEvent struct {
	Action  string
	Test    string
	Output  string
	Elapsed float64
}

// goTest runs go test in this package with the arguments and env added to the
// process environment, and returns the events it printed.
func goTest(t *testing.T, env []string, args ...string) []testEvent {
	t.Helper()
	return goTestIn(t, append(os.Environ(), env...), args...)
}

// goTestIn runs go test in this package with the arguments and exactly the
// environment given, and returns the events it printed. It skips the calling
// test when it is itself such a run, or when go is not on PATH.
func goTestIn(t *testing.T, environ []string, args ...string) []testEvent {
	t.Helper()
	if os.Getenv(childEnv) != "" {
		t.Skip("this is a go test run started by another test of the package")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go is not on PATH")
	}
	cmd := exec.Command(goBin, append([]string{"test", "-count=1", "-json"}, args...)...)
	cmd.Env = append(slices.Clone(environ), childEnv+"=1")
	out, err := cmd.Output()
	var stderr []byte
	if ee, ok := err.(*exec.ExitError); ok {
		stderr = ee.Stderr
	}
	var events []testEvent
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var e testEvent
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			events = append(events, e)
		}
	}
	if err != nil {
		var failed []string
		for _, e := range events {
			if e.Action == "fail" && e.Test != "" {
				failed = append(failed, e.Test)
			}
		}
		t.Fatalf("go test %s: %v\nfailed: %v\n%s", strings.Join(args, " "), err, failed, stderr)
	}
	return events
}

// conformanceSubtests are the tests of events that ended in action and are
// named TestProviderConformance/<provider>/<case>.
func conformanceSubtests(events []testEvent, action string) []string {
	var out []string
	for _, e := range events {
		if e.Action == action && strings.HasPrefix(e.Test, "TestProviderConformance/") && strings.Count(e.Test, "/") == 2 {
			out = append(out, e.Test)
		}
	}
	slices.Sort(out)
	return out
}

// allSubtests are the 21 names the spec describes.
func allSubtests() []string {
	var out []string
	for _, p := range specProviders {
		for _, c := range specCases {
			out = append(out, "TestProviderConformance/"+p+"/"+c)
		}
	}
	slices.Sort(out)
	return out
}

// TS-30-2 (30-REQ-1.2): go test -short runs 21 subtests named
// <provider>/<case>, none skipped, and a -run pattern selects exactly one.
func TestSuiteRunsTwentyOneNamedSubtestsUnderShort_TS30_2(t *testing.T) {
	events := goTest(t, nil, "-short", "-v", "-run", "^TestProviderConformance$", ".")
	if got, want := conformanceSubtests(events, "pass"), allSubtests(); !slices.Equal(got, want) {
		t.Errorf("the passing subtests are\n  %v\nwant the 21\n  %v", got, want)
	}
	if skipped := conformanceSubtests(events, "skip"); len(skipped) != 0 {
		t.Errorf("subtests skipped under -short: %v", skipped)
	}

	events = goTest(t, nil, "-short", "-v", "-run", "TestProviderConformance.*/ollama/thinking", ".")
	if got, want := conformanceSubtests(events, "pass"), []string{"TestProviderConformance/ollama/thinking"}; !slices.Equal(got, want) {
		t.Errorf("-run 'TestProviderConformance.*/ollama/thinking' passed %v, want exactly %v", got, want)
	}
}

// TS-30-3 (30-REQ-1.3): each subtest builds its agent with agentkit.New from the
// provider's name, the tier STANDARD, one tool echo and four turns, with a
// Getenv that holds only that provider's variables, every one pointing at the
// subtest's own fake, which then receives at least one request.
func TestEverySubtestBuildsItsAgentFromItsOwnFake_TS30_3(t *testing.T) {
	vars := map[string][]string{
		"anthropic":  {"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL"},
		"openrouter": {"OPENROUTER_API_KEY", "OPENROUTER_BASE_URL"},
		"ollama":     {"OLLAMA_HOST"},
	}
	owned := map[string][]string{
		"anthropic":  {"ANTHROPIC_", "CLAUDE_CODE_", "GOOGLE_CLOUD_", "CLOUD_ML_REGION", "CLOUDSDK_", "AWS_"},
		"openrouter": {"OPENROUTER_"},
		"ollama":     {"OLLAMA_"},
	}
	urlVars := map[string]bool{"ANTHROPIC_BASE_URL": true, "OPENROUTER_BASE_URL": true, "OLLAMA_HOST": true}
	for _, p := range providerTable {
		for _, c := range conformanceCases {
			t.Run(p.name+"/"+c.name, func(t *testing.T) {
				t.Parallel()
				r := runCase(t, p, c, runOptions{})
				cfg := r.cfg
				if cfg.Provider != p.name || cfg.Model != "STANDARD" || cfg.MaxTurns != 4 {
					t.Errorf("agentkit.New got Provider %q, Model %q, MaxTurns %d; want %q, STANDARD and 4", cfg.Provider, cfg.Model, cfg.MaxTurns, p.name)
				}
				if tools := cfg.Toolset.Tools; len(tools) != 1 || tools[0].Name != "echo" {
					t.Errorf("agentkit.New got the tools %v, want exactly echo", tools)
				}
				if cfg.Client != nil || cfg.Env != nil || cfg.Getenv == nil {
					t.Errorf("agentkit.New got Client %v, Env %v and Getenv set %v; want the provider built from a Getenv alone", cfg.Client, cfg.Env, cfg.Getenv != nil)
				}

				var keys []string
				for k, v := range r.env.vars {
					keys = append(keys, k)
					if urlVars[k] && !strings.HasPrefix(v, r.fake.URL()) {
						t.Errorf("%s=%s does not point at this subtest's fake %s", k, v, r.fake.URL())
					}
				}
				slices.Sort(keys)
				if !slices.Equal(keys, vars[p.name]) {
					t.Errorf("the Getenv map holds %v, want only %v", keys, vars[p.name])
				}
				if p.name == "openrouter" && !strings.HasSuffix(r.env.vars["OPENROUTER_BASE_URL"], "/api/v1") {
					t.Errorf("OPENROUTER_BASE_URL is %s, want it to end in /api/v1", r.env.vars["OPENROUTER_BASE_URL"])
				}

				asked := r.env.Asked()
				if len(asked) == 0 {
					t.Error("the provider asked for no variable")
				}
				for _, name := range asked {
					if !slices.ContainsFunc(owned[p.name], func(prefix string) bool { return strings.HasPrefix(name, prefix) }) {
						t.Errorf("the provider asked for %s, which is not one of %s's variables", name, p.name)
					}
					// What it asked for is unset or points at this fake: the Getenv
					// has no other value to give.
					if v := r.env.vars[name]; urlVars[name] && v != "" && !strings.HasPrefix(v, r.fake.URL()) {
						t.Errorf("%s resolves to %s, not to this subtest's fake", name, v)
					}
				}
				if len(r.requests) < 1 {
					t.Error("the fake recorded no request")
				}
			})
		}
	}
}

// TS-30-4 (30-REQ-1.4): the suite has the same outcome when the process
// environment holds other values for every variable a provider reads. A run of
// the suite with them all pointing at a decoy listener passes 21 subtests and
// leaves the decoy with no connection.
func TestOutcomesDoNotDependOnTheProcessEnvironment_TS30_4(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback listener: %v", err)
	}
	defer ln.Close()
	var accepted atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			c.Close()
		}
	}()
	addr := ln.Addr().String()
	env := []string{
		"ANTHROPIC_API_KEY=decoy-key", "ANTHROPIC_AUTH_TOKEN=decoy-token", "ANTHROPIC_BASE_URL=http://" + addr,
		"CLAUDE_CODE_USE_VERTEX=1", "ANTHROPIC_VERTEX_PROJECT_ID=decoy-project", "ANTHROPIC_VERTEX_BASE_URL=http://" + addr,
		"OPENROUTER_API_KEY=decoy-key", "OPENROUTER_BASE_URL=http://" + addr + "/api/v1",
		"OLLAMA_HOST=" + addr,
	}
	events := goTest(t, env, "-short", "-v", "-run", "^TestProviderConformance$", ".")
	if got, want := conformanceSubtests(events, "pass"), allSubtests(); !slices.Equal(got, want) {
		t.Errorf("with the decoy environment the passing subtests are\n  %v\nwant the 21\n  %v", got, want)
	}
	// A late connection may still be on its way in.
	time.Sleep(50 * time.Millisecond)
	if n := accepted.Load(); n != 0 {
		t.Errorf("the decoy listener accepted %d connections, want none: some subtest reached an address of the process environment", n)
	}
}

// 30-REQ-1.4 and TS-30-4, in the source: no file of the conformance suite reads
// or sets the process environment, and TestProviderConformance does not skip.
// The files that run go test as a child (this one) and the live tests, which
// are meant to read the environment, are not part of the suite.
func TestSuiteSourceNeverTouchesTheProcessEnvironment_TS30_4(t *testing.T) {
	banned := map[string]bool{"Getenv": true, "LookupEnv": true, "Environ": true, "Setenv": true, "Unsetenv": true, "Clearenv": true, "ExpandEnv": true}
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, file := range files {
		if file == "suite_test.go" || strings.HasPrefix(file, "live") || file == "smoke_test.go" || file == "layout_test.go" {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && (id.Name == "os" || id.Name == "t" || id.Name == "tb") && banned[sel.Sel.Name] {
				t.Errorf("%s uses %s.%s", file, id.Name, sel.Sel.Name)
			}
			return true
		})
	}
	if checked < 8 {
		t.Errorf("checked %d files, want the whole suite", checked)
	}

	f, err := parser.ParseFile(token.NewFileSet(), "cases_test.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "TestProviderConformance" {
			continue
		}
		found = true
		ast.Inspect(fn, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && (strings.HasPrefix(sel.Sel.Name, "Skip") || sel.Sel.Name == "Short") {
				t.Errorf("TestProviderConformance uses %s: it runs under -short", sel.Sel.Name)
			}
			return true
		})
	}
	if !found {
		t.Error("cases_test.go has no TestProviderConformance")
	}
}
