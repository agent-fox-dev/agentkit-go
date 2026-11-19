package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
)

// writeRequest writes body to a request file outside dir and returns its path.
func writeRequest(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// callResult is the object `call` writes.
type callResult struct {
	OK     bool           `json:"ok"`
	Data   map[string]any `json:"data"`
	Error  string         `json:"error"`
	Detail string         `json:"detail"`
	Text   string         `json:"text"`
}

// parseCall decodes stdout as exactly one JSON object.
func parseCall(t *testing.T, stdout string) callResult {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(stdout))
	var res callResult
	if err := dec.Decode(&res); err != nil {
		t.Fatalf("stdout is not a JSON object: %v\n%s", err, stdout)
	}
	if dec.More() {
		t.Fatalf("stdout holds more than one JSON value:\n%s", stdout)
	}
	return res
}

func TestACallWritesOneResultObjectWithTheServedText(t *testing.T) {
	dir := sampleTree(t)
	s, err := open(settings{dir: dir}, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	for _, c := range []struct {
		name, body string
	}{
		{"read a file", `{"name":"read_file","input":{"path":"a.go"}}`},
		{"read a missing file", `{"name":"read_file","input":{"path":"missing.go"}}`},
		{"no input", `{"name":"list_files"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			res := run(t, "call", "--dir", dir, "--request", writeRequest(t, c.body))
			if res.exit != 0 {
				t.Fatalf("exit %d, stderr %s", res.exit, res.stderr)
			}
			got := parseCall(t, res.stdout)
			var req struct {
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			}
			_ = json.Unmarshal([]byte(c.body), &req)
			want := s.runner.Call(context.Background(), req.Name, req.Input)
			if got.OK != want.OK || got.Error != want.Error || got.Text != want.LLMText() {
				t.Fatalf("got ok %v error %q text %q; want ok %v error %q text %q", got.OK, got.Error, got.Text, want.OK, want.Error, want.LLMText())
			}
			wantData, _ := json.Marshal(want.Data)
			gotData, _ := json.Marshal(got.Data)
			if !testutil.SameJSON(wantData, gotData) {
				t.Errorf("data = %s, want %s", gotData, wantData)
			}
		})
	}

	blocks := core.ToolResult{OK: true, Text: "served", Blocks: []core.ContentBlock{core.TextBlock{Text: "first"}, core.TextBlock{Text: "second"}}}
	if got := servedText(blocks); got != "served\nfirst\nsecond" {
		t.Errorf("servedText = %q", got)
	}
}

func TestABadRequestOrTimeoutMakesNoCall(t *testing.T) {
	dir := sampleTree(t)
	good := writeRequest(t, `{"name":"read_file","input":{"path":"a.go"}}`)
	big := filepath.Join(t.TempDir(), "big.json")
	if err := os.WriteFile(big, make([]byte, 16<<20+1), 0o644); err != nil {
		t.Fatal(err)
	}
	unreadable := writeRequest(t, `{"name":"read_file"}`)
	if err := os.Chmod(unreadable, 0); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		args []string
	}{
		{"no such file", []string{"--request", filepath.Join(dir, "missing.json")}},
		{"over 16 MiB", []string{"--request", big}},
		{"an array", []string{"--request", writeRequest(t, `[1]`)}},
		{"no name", []string{"--request", writeRequest(t, `{"input":{}}`)}},
		{"an empty name", []string{"--request", writeRequest(t, `{"name":""}`)}},
		{"a name that is a number", []string{"--request", writeRequest(t, `{"name":7}`)}},
		{"no request", nil},
		{"a timeout that does not parse", []string{"--request", good, "--timeout", "soon"}},
		{"a zero timeout", []string{"--request", good, "--timeout", "0s"}},
		{"a negative timeout", []string{"--request", good, "--timeout", "-1s"}},
	}
	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		cases = append(cases, struct {
			name string
			args []string
		}{"unreadable", []string{"--request", unreadable}})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := run(t, append([]string{"call", "--dir", dir}, c.args...)...)
			if res.exit != 2 || res.stdout != "" {
				t.Fatalf("exit %d, stdout %q", res.exit, res.stdout)
			}
			if l := lines(res.stderr); len(l) != 1 {
				t.Fatalf("stderr = %q, want one line", res.stderr)
			}
		})
	}
}

func TestAnInputThatIsNotAnObjectIsInvalidArguments(t *testing.T) {
	res := run(t, "call", "--dir", sampleTree(t), "--request", writeRequest(t, `{"name":"read_file","input":[1]}`))
	if res.exit != 0 {
		t.Fatalf("exit %d, stderr %s", res.exit, res.stderr)
	}
	if got := parseCall(t, res.stdout); got.OK || got.Error != "invalid_arguments" {
		t.Fatalf("ok %v, error %q; want invalid_arguments", got.OK, got.Error)
	}
}

func TestANameThatIsNotServedIsUnknownTool(t *testing.T) {
	dir := sampleTree(t)
	for _, c := range []struct {
		name, body string
		flags      []string
	}{
		{"no such tool", `{"name":"no_such_tool"}`, nil},
		{"left out by --read-only", `{"name":"write_file","input":{"path":"made.txt","content":"x"}}`, []string{"--read-only"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			res := run(t, append([]string{"call", "--dir", dir, "--request", writeRequest(t, c.body)}, c.flags...)...)
			if res.exit != 0 {
				t.Fatalf("exit %d, stderr %s", res.exit, res.stderr)
			}
			if got := parseCall(t, res.stdout); got.OK || got.Error != "unknown_tool" {
				t.Fatalf("ok %v, error %q; want unknown_tool", got.OK, got.Error)
			}
			if _, err := os.Stat(filepath.Join(dir, "made.txt")); err == nil {
				t.Fatal("a tool that is not served wrote a file")
			}
		})
	}
}

func TestACallPastItsTimeoutIsATimeoutResult(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	req := writeRequest(t, `{"name":"execute","input":{"command":"sleep 60","timeout_s":120}}`)
	var res result
	if !testutil.Within(30*time.Second, func() bool {
		res = run(t, "call", "--dir", sampleTree(t), "--request", req, "--timeout", "1s")
		return true
	}) {
		t.Fatal("the call outlived its timeout by far")
	}
	if res.exit != 0 {
		t.Fatalf("exit %d, stderr %s", res.exit, res.stderr)
	}
	got := parseCall(t, res.stdout)
	if got.OK || got.Error != "timeout" || !strings.Contains(got.Detail, "1s") {
		t.Fatalf("ok %v, error %q, detail %q; want a timeout naming 1s", got.OK, got.Error, got.Detail)
	}
}

func TestARelayListsAndCallsInSingleProcesses(t *testing.T) {
	dir := sampleTree(t)
	l := run(t, "tools", "--dir", dir)
	if l.exit != 0 {
		t.Fatalf("tools: exit %d, stderr %s", l.exit, l.stderr)
	}
	names := parseListing(t, l.stdout).names()
	if len(names) == 0 || names[len(names)-1] != "code_mode" {
		t.Fatalf("listing = %v", names)
	}
	if slices.Contains(names, "code_search") {
		res := run(t, "call", "--dir", dir, "--request", writeRequest(t, `{"name":"code_search","input":{"query":"needle"}}`))
		if res.exit != 0 {
			t.Fatalf("code_search: exit %d, stderr %s", res.exit, res.stderr)
		}
		if got := parseCall(t, res.stdout); !got.OK || !strings.Contains(got.Text, "a.go") {
			t.Fatalf("code_search: ok %v, text %q", got.OK, got.Text)
		}
	}
	res := run(t, "call", "--dir", dir, "--request", writeRequest(t, `{"name":"nope"}`))
	if res.exit != 0 {
		t.Fatalf("nope: exit %d", res.exit)
	}
	if got := parseCall(t, res.stdout); got.OK || got.Error != "unknown_tool" {
		t.Fatalf("nope: ok %v, error %q", got.OK, got.Error)
	}
}

func TestACodeModeScriptRunsItsCallsInOneProcess(t *testing.T) {
	dir := sampleTree(t)
	search := `h = code_search(query="needle")` + "\n"
	searches := 1
	if !slices.Contains(parseListing(t, run(t, "tools", "--dir", dir).stdout).names(), "code_search") {
		search, searches = "", 0
	}
	script := search + `files = parallel([call(read_file, path=p) for p in ["a.go", "b.go", "c.go"]])
print("read", len(files))
result = {"files": len(files)}`
	body, _ := json.Marshal(map[string]any{"name": "code_mode", "input": map[string]string{"script": script}})
	res := run(t, "call", "--dir", dir, "--request", writeRequest(t, string(body)))
	if res.exit != 0 {
		t.Fatalf("exit %d, stderr %s", res.exit, res.stderr)
	}
	got := parseCall(t, res.stdout)
	if !got.OK {
		t.Fatalf("code_mode failed: %s %s %s", got.Error, got.Detail, got.Text)
	}
	if got.Data["output"] != "read 3" {
		t.Errorf("output = %q", got.Data["output"])
	}
	if !reflect.DeepEqual(got.Data["return_value"], map[string]any{"files": float64(3)}) {
		t.Errorf("return_value = %v", got.Data["return_value"])
	}
	if calls, _ := got.Data["calls_completed"].([]any); len(calls) != 3+searches {
		t.Errorf("calls_completed = %v, want %d calls", got.Data["calls_completed"], 3+searches)
	}
	s, err := open(settings{dir: dir}, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	input, _ := json.Marshal(map[string]string{"script": script})
	if want := s.runner.Call(context.Background(), "code_mode", input).LLMText(); got.Text != want || !strings.HasPrefix(got.Text, "read 3") {
		t.Errorf("text = %q, want %q", got.Text, want)
	}
}
