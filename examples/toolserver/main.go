// Command toolserver shows agentkit-tools, the command that serves AgentKit's
// tools, code_search and code_mode for one directory, as another harness or
// a relay uses it:
//
//	cd examples/toolserver && go run .
//
// It builds the command, writes a sample project to a temporary directory,
// lists the served tools, works through an MCP session on the command's
// stdin and stdout (search, edit, search again), then makes single calls from
// request files, as a relay that cannot hold a session does. It runs offline,
// with no API key, and removes what it wrote.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	agentmcp "github.com/agent-fox-dev/agentkit-go/mcp"
	"github.com/agent-fox-dev/agentkit-go/wire"
)

func main() {
	if err := run(os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "toolserver:", err)
		os.Exit(1)
	}
}

func run(out io.Writer) error {
	tmp, err := os.MkdirTemp("", "agentkit-toolserver-example-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	// The command is a tool of this module (the `tool` line of go.mod), so
	// the go command builds it at the version this module requires.
	bin := filepath.Join(tmp, "agentkit-tools")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if b, err := exec.Command("go", "build", "-o", bin, "github.com/agent-fox-dev/agentkit-go/cmd/agentkit-tools").CombinedOutput(); err != nil {
		return fmt.Errorf("building agentkit-tools: %v\n%s", err, b)
	}
	dir := filepath.Join(tmp, "sample")
	for name, body := range sample {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			return err
		}
	}

	// 1. The served tools, as a relay answers tools/list.
	names, err := listTools(bin, dir)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "agentkit-tools serves %d tools: %s\n", len(names), strings.Join(names, ", "))
	search := slices.Contains(names, "code_search")
	if !search {
		fmt.Fprintln(out, "code_search is not served on this platform; the searches are left out")
	}

	// 2. A session, as a harness beside the directory holds one.
	if err := session(out, bin, dir, search); err != nil {
		return err
	}

	// 3. Single calls, one process each, as a relay that can only start a
	// process and read its output makes them.
	if search {
		res, err := call(bin, dir, map[string]any{"name": "code_search", "input": map[string]any{"query": "needle"}})
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "call: code_search for needle found %s\n", firstFile(res.Text))
	}
	script := `files = parallel([call(read_file, path=p) for p in ["a.go", "b.go", "c.go"]])
print("read", len(files), "files")
result = len(files)`
	res, err := call(bin, dir, map[string]any{"name": "code_mode", "input": map[string]any{"script": script}})
	if err != nil {
		return err
	}
	if !res.OK {
		return fmt.Errorf("code_mode: %s: %s", res.Error, res.Detail)
	}
	fmt.Fprintf(out, "call: code_mode %s\n", firstLine(res.Text))
	res, err = call(bin, dir, map[string]any{"name": "no_such_tool"})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "call: an unknown tool is a result too: ok=%v error=%s\n", res.OK, res.Error)

	fmt.Fprintln(out, "toolserver example completed successfully")
	return nil
}

// listTools runs `agentkit-tools tools` and returns the names it lists.
func listTools(bin, dir string) ([]string, error) {
	b, err := exec.Command(bin, "tools", "--dir", dir).Output()
	if err != nil {
		return nil, fmt.Errorf("agentkit-tools tools: %v", err)
	}
	var defs []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(b, &defs); err != nil {
		return nil, err
	}
	var names []string
	for _, d := range defs {
		names = append(names, d.Name)
	}
	return names, nil
}

// session starts `agentkit-tools mcp`, connects AgentKit's MCP client to its
// stdin and stdout, searches, edits a file and searches again, then closes
// the session and waits for the command to exit.
func session(out io.Writer, bin, dir string, search bool) error {
	cmd := exec.Command(bin, "mcp", "--dir", dir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _, _ = io.Copy(io.Discard, bufio.NewReader(stderr)) }() // the command's logs
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	conn, err := agentmcp.Connect(ctx, agentmcp.ServerConfig{Name: "agentkit-tools", Timeout: time.Minute},
		agentmcp.NewPipeTransport(stdout, stdin, wire.Defaults()), agentmcp.ConnectionOptions{})
	if err != nil {
		_ = cmd.Process.Kill()
		return fmt.Errorf("connecting to agentkit-tools mcp: %v", err)
	}
	if search {
		text, err := callText(ctx, conn, "code_search", map[string]any{"query": "needle"})
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "session: code_search for needle found %s\n", firstFile(text))
	}
	if _, err := callText(ctx, conn, "edit_file", map[string]any{"path": "b.go", "edits": []any{map[string]any{
		"old_string": "func Other() int { return 2 }", "new_string": "func Other() int { return 2 } // fresh_marker"}}}); err != nil {
		return err
	}
	if search {
		text, err := callText(ctx, conn, "code_search", map[string]any{"query": "fresh_marker"})
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "session: after edit_file, code_search for fresh_marker found %s\n", firstFile(text))
	}
	_ = conn.Close()
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("agentkit-tools mcp: %v", err)
	}
	fmt.Fprintln(out, "session: closed; agentkit-tools exited 0")
	return nil
}

// callText is the text of one tools/call, or an error for a failed call.
func callText(ctx context.Context, conn *agentmcp.ServerConnection, name string, args map[string]any) (string, error) {
	res, err := conn.Call(ctx, name, args)
	if err != nil {
		return "", fmt.Errorf("%s: %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*agentmcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	if res.IsError {
		return "", fmt.Errorf("%s failed: %s", name, b.String())
	}
	return b.String(), nil
}

// result is what `agentkit-tools call` writes.
type result struct {
	OK     bool   `json:"ok"`
	Error  string `json:"error"`
	Detail string `json:"detail"`
	Text   string `json:"text"`
}

// call writes req to a request file and runs `agentkit-tools call` on it.
func call(bin, dir string, req map[string]any) (result, error) {
	f, err := os.CreateTemp("", "agentkit-toolserver-example-request-*.json")
	if err != nil {
		return result{}, err
	}
	defer os.Remove(f.Name())
	if err := json.NewEncoder(f).Encode(req); err != nil {
		_ = f.Close()
		return result{}, err
	}
	if err := f.Close(); err != nil {
		return result{}, err
	}
	b, err := exec.Command(bin, "call", "--dir", dir, "--request", f.Name()).Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return result{}, fmt.Errorf("agentkit-tools call: exit %d: %s", ee.ExitCode(), ee.Stderr)
	}
	if err != nil {
		return result{}, err
	}
	var res result
	return res, json.Unmarshal(b, &res)
}

// firstFile is the first file a code_search result names.
func firstFile(text string) string {
	for _, l := range strings.Split(text, "\n") {
		if f, _, ok := strings.Cut(strings.TrimSpace(l), " ("); ok && strings.HasSuffix(f, ".go") {
			return f
		}
	}
	return "nothing"
}

// firstLine is the first line of s.
func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}
