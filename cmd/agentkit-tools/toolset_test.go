package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/codemode"
	"github.com/agent-fox-dev/agentkit-go/codesearch"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

var builtinNames = []string{"read_file", "write_file", "edit_file", "list_files", "find_files", "search_files",
	"file_outline", "find_symbol", "find_references", "execute", "run_command"}

// wantTools is the tool set the packages themselves build over dir: the
// built-in tools, code_search where the platform has it, then code_mode over
// all of them.
func wantTools(t *testing.T, dir string) []core.Tool {
	t.Helper()
	ws, err := workspace.Open(dir, workspace.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var opts tools.Options
	idx, err := codesearch.New(ws, codesearch.Options{})
	switch {
	case errors.Is(err, codesearch.ErrUnsupported):
	case err != nil:
		t.Fatal(err)
	default:
		t.Cleanup(func() { _ = idx.Close() })
		opts.Index = idx
	}
	ts, err := tools.For(ws, opts)
	if err != nil {
		t.Fatal(err)
	}
	cm, _, err := codemode.New(ts, codemode.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return append(ts, cm)
}

func marshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTheDirectoryIsServedWithThePackagesToolsInServingOrder(t *testing.T) {
	dir := sampleTree(t)
	want := wantTools(t, dir)
	for _, c := range []struct {
		name string
		res  func() result
	}{
		{"--dir", func() result { return run(t, "tools", "--dir", dir) }},
		{"working directory", func() result { return runIn(t, dir, testEnv(), "tools") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			res := c.res()
			if res.exit != 0 {
				t.Fatalf("exit %d, stderr %s", res.exit, res.stderr)
			}
			got := parseListing(t, res.stdout)
			var wantNames []string
			for _, w := range want {
				wantNames = append(wantNames, w.Name)
			}
			if !slices.Equal(got.names(), wantNames) {
				t.Fatalf("names = %v, want %v", got.names(), wantNames)
			}
			for _, w := range want {
				d := got.get(w.Name)
				if desc := marshal(t, w.Description); string(d["description"]) != desc {
					t.Errorf("%s: description differs from the package's", w.Name)
				}
				if in := marshal(t, w.InputSchema); string(d["input_schema"]) != in {
					t.Errorf("%s: input_schema = %s, want %s", w.Name, d["input_schema"], in)
				}
				if w.OutputSchema != nil {
					if out := marshal(t, w.OutputSchema); string(d["output_schema"]) != out {
						t.Errorf("%s: output_schema = %s, want %s", w.Name, d["output_schema"], out)
					}
				}
			}
		})
	}
}

func TestADirectoryThatIsMissingOrAFileServesNothing(t *testing.T) {
	dir := sampleTree(t)
	req := filepath.Join(t.TempDir(), "req.json")
	if err := os.WriteFile(req, []byte(`{"name":"read_file","input":{"path":"a.go"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, sub := range [][]string{{"mcp"}, {"call", "--request", req}, {"tools"}} {
		for _, path := range []string{filepath.Join(dir, "missing"), filepath.Join(dir, "a.go")} {
			t.Run(sub[0]+"/"+filepath.Base(path), func(t *testing.T) {
				res := run(t, append(slices.Clone(sub), "--dir", path)...)
				if res.exit != 2 || res.stdout != "" {
					t.Fatalf("exit %d, stdout %q", res.exit, res.stdout)
				}
				if l := lines(res.stderr); len(l) != 1 || !strings.Contains(l[0], path) {
					t.Fatalf("stderr = %q, want one line naming %s", res.stderr, path)
				}
			})
		}
	}
}

func TestWithoutCodeSearchOnThePlatformTheOtherToolsAreServed(t *testing.T) {
	old := newIndex
	newIndex = func(*workspace.Workspace) (tools.Index, error) { return nil, codesearch.ErrUnsupported }
	t.Cleanup(func() { newIndex = old })

	res := inProcess("tools", "--dir", sampleTree(t))
	if res.exit != 0 {
		t.Fatalf("exit %d, stderr %s", res.exit, res.stderr)
	}
	got := parseListing(t, res.stdout)
	if want := append(slices.Clone(builtinNames), "code_mode"); !slices.Equal(got.names(), want) {
		t.Fatalf("names = %v, want %v", got.names(), want)
	}
	if strings.Contains(string(got.get("code_mode")["description"]), "code_search") {
		t.Error("code_mode's description declares code_search")
	}
	if l := lines(res.stderr); len(l) != 1 || !strings.Contains(l[0], "code_search") {
		t.Fatalf("stderr = %q, want one line naming code_search", res.stderr)
	}
}

func TestTheShellToolsGetTheReducedEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	t.Setenv("ANTHROPIC_API_KEY", "sk-fake")
	s, err := open(settings{dir: sampleTree(t)}, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	res := s.runner.Call(context.Background(), "execute", json.RawMessage(`{"command":"env"}`))
	if !res.OK {
		t.Fatalf("execute env: %s %s", res.Error, res.Detail)
	}
	text := res.LLMText()
	if !strings.Contains(text, "PATH=") || strings.Contains(text, "ANTHROPIC_API_KEY") {
		t.Fatalf("the shell's environment is not the reduced one:\n%s", text)
	}
}

func TestVersionPrintsTheModuleVersion(t *testing.T) {
	res := run(t, "--version")
	if res.exit != 0 || res.stdout != "(devel)\n" || res.stderr != "" {
		t.Fatalf("--version: exit %d, stdout %q, stderr %q", res.exit, res.stdout, res.stderr)
	}
	for _, c := range []struct {
		bi   *debug.BuildInfo
		want string
	}{
		{nil, "(devel)"},
		{&debug.BuildInfo{Main: debug.Module{Path: selfPath, Version: "v1.2.3"}}, "v1.2.3"},
		{&debug.BuildInfo{Main: debug.Module{Path: "example.test/x"}, Deps: []*debug.Module{{Path: selfPath, Version: "v0.4.0"}}}, "v0.4.0"},
		{&debug.BuildInfo{Main: debug.Module{Path: "example.test/x"}, Deps: []*debug.Module{{Path: selfPath, Version: "v0.4.0", Replace: &debug.Module{Version: "v0.5.0"}}}}, "v0.5.0"},
		{&debug.BuildInfo{Main: debug.Module{Path: selfPath}}, "(devel)"},
	} {
		if got := versionFrom(c.bi); got != c.want {
			t.Errorf("versionFrom = %q, want %q", got, c.want)
		}
	}
}

func TestAMisspelledCommandLinePrintsUsage(t *testing.T) {
	req := filepath.Join(t.TempDir(), "req.json")
	if err := os.WriteFile(req, []byte(`{"name":"read_file"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{}, {"serve"}, {"tools", "--bogus"}, {"mcp", "--request", req}, {"tools", "--timeout", "1s"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			res := run(t, args...)
			if res.exit != 2 || res.stdout != "" {
				t.Fatalf("exit %d, stdout %q", res.exit, res.stdout)
			}
			for _, w := range []string{"mcp", "call", "tools"} {
				if !strings.Contains(res.stderr, w) {
					t.Errorf("usage does not name %s:\n%s", w, res.stderr)
				}
			}
		})
	}
}
