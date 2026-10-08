package tools_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentfox/agentkit-go/examples/tools/toolcli"
	"github.com/agentfox/agentkit-go/tools"
)

// programs is every directory under examples/tools, one per built-in tool.
var programs = []string{
	"read_file", "write_file", "edit_file", "list_files", "find_files", "search_files",
	"file_outline", "find_symbol", "find_references",
	"execute", "run_command", "powershell", "fetch_url",
}

// TestEveryProgramRunsItsOwnTool pins the one line each main.go consists of:
// the directory name is the tool name the model sees, and the program runs
// that tool and no other.
func TestEveryProgramRunsItsOwnTool(t *testing.T) {
	for _, name := range programs {
		src, err := os.ReadFile(filepath.Join(name, "main.go"))
		if err != nil {
			t.Fatal(err)
		}
		if want := `toolcli.Main("` + name + `")`; !bytes.Contains(src, []byte(want)) {
			t.Errorf("%s/main.go does not call %s", name, want)
		}
		stdout, _, code := run(t, t.TempDir(), name, "", "-schema")
		if code != toolcli.ExitOK || !strings.HasPrefix(stdout, "name: "+name+"\n") || !strings.Contains(stdout, "input schema:") {
			t.Errorf("%s -schema: exit %d, output:\n%s", name, code, stdout)
		}
	}
}

// TestToolsAgainstAWorkspace calls each tool once with arguments a model
// might send, in order, against one temporary workspace. Nothing here needs
// the network or a key: fetch_url is exercised up to its scheme check.
func TestToolsAgainstAWorkspace(t *testing.T) {
	ws := t.TempDir()
	steps := []struct {
		tool  string
		flags []string
		args  string
		code  int
		want  string // substring of stdout
	}{
		{"write_file", nil, `{"path":"greet/greet.go","content":"package greet\n\n// Greet says hello.\nfunc Greet(name string) string { return \"hello \" + name }\n\nfunc Twice() string { return Greet(\"a\") + Greet(\"b\") }\n"}`, 0, "Wrote 150 bytes to greet/greet.go"},
		{"edit_file", nil, `{"path":"greet/greet.go","edits":[{"old_string":"\"hello \"","new_string":"\"hi \""}]}`, 0, "Applied 1 edit to greet/greet.go"},
		{"read_file", nil, `{"path":"greet/greet.go","limit":"4"}`, 0, `return "hi " + name`}, // "4" is coerced, as for a model
		{"list_files", nil, `{}`, 0, "greet/"},
		{"find_files", nil, `{"pattern":"**/*.go"}`, 0, "greet/greet.go"},
		{"search_files", nil, `{"pattern":"func \\w+","file_glob":"**/*.go"}`, 0, "func Greet"},
		{"file_outline", nil, `{"path":"greet/greet.go"}`, 0, "func Greet(name string) string"},
		{"find_symbol", nil, `{"name":"Greet","exact":true}`, 0, "greet/greet.go"},
		{"find_references", nil, `{"name":"Greet"}`, 0, "greet/greet.go"},
		{"execute", []string{"-allow", "echo"}, `{"command":"echo from execute"}`, 0, "from execute"},
		{"run_command", []string{"-allow", "echo"}, `{"argv":["echo","from run_command"]}`, 0, "from run_command"},
		{"fetch_url", nil, `{"url":"http://127.0.0.1/"}`, 1, "scheme_not_allowed"},

		// What the model gets back when something is wrong.
		{"read_file", nil, `{}`, 1, "invalid_arguments"},
		{"read_file", nil, `{"path":"../outside.txt"}`, 1, "path_not_allowed"},
		{"edit_file", nil, `{"path":"greet/greet.go","edits":[{"old_string":"absent","new_string":"x"}]}`, 1, "edit_not_found"},
		{"execute", nil, `{"command":"echo not on the allowlist"}`, 1, "blocked_by_policy"},
		{"execute", []string{"-allow", "echo"}, `{"command":"echo a | cat"}`, 1, "blocked_by_policy"},
		{"powershell", nil, `{"command":"Get-ChildItem"}`, 1, "blocked_by_policy"},
	}
	for _, s := range steps {
		stdout, stderr, code := run(t, ws, s.tool, s.args, s.flags...)
		if code != s.code || !strings.Contains(stdout, s.want) {
			t.Errorf("%s %s %s: exit %d (want %d), want %q in stdout\nstdout:\n%s\nstderr:\n%s",
				s.tool, strings.Join(s.flags, " "), s.args, code, s.code, s.want, stdout, stderr)
		}
	}
}

func TestPowerShellWhenInstalled(t *testing.T) {
	if _, err := tools.ResolvePowerShell(); err != nil {
		t.Skip("pwsh not installed:", err)
	}
	stdout, stderr, code := run(t, t.TempDir(), "powershell", `{"command":"Write-Output 'from pwsh'"}`, "-allow-all")
	if code != toolcli.ExitOK || !strings.Contains(stdout, "from pwsh") {
		t.Errorf("exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
}

// TestStdinDataAndUsage covers the rest of the contract: arguments on stdin,
// the -data dump on stderr, and a usage error for input that is not JSON.
func TestStdinDataAndUsage(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := toolcli.Run(context.Background(), "read_file", toolcli.Builtins,
		[]string{"-dir", ws, "-data"}, strings.NewReader(`{"path":"a.txt"}`), &stdout, &stderr)
	if code != toolcli.ExitOK || stdout.String() != "one\ntwo\n" {
		t.Errorf("stdin call: exit %d, stdout %q", code, stdout.String())
	}
	if !strings.Contains(stderr.String(), `"ok": true`) || !strings.Contains(stderr.String(), `"content": "one\ntwo"`) {
		t.Errorf("-data did not print the structured result on stderr:\n%s", stderr.String())
	}

	_, _, code = run(t, ws, "read_file", "not json")
	if code != toolcli.ExitUsage {
		t.Errorf("non-JSON arguments: exit %d, want %d", code, toolcli.ExitUsage)
	}
}

func run(t *testing.T, ws, tool, args string, flags ...string) (stdout, stderr string, code int) {
	t.Helper()
	argv := append([]string{"-dir", ws}, flags...)
	if args != "" {
		argv = append(argv, args)
	}
	var out, errb bytes.Buffer
	code = toolcli.Run(context.Background(), tool, toolcli.Builtins, argv, strings.NewReader(""), &out, &errb)
	return out.String(), errb.String(), code
}
