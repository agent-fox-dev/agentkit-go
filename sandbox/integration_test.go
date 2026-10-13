package sandbox_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// sameDefinition reports how p's definition differs from h's, or "".
func sameDefinition(t *testing.T, p, h core.Tool) string {
	t.Helper()
	switch {
	case p.Name != h.Name, p.Description != h.Description, p.Label != h.Label,
		!slices.Equal(p.PromptGuidelines, h.PromptGuidelines), p.Builtin != h.Builtin, p.ExecutionMode != h.ExecutionMode:
		return "a definition field"
	case mustJSON(t, p.InputSchema) != mustJSON(t, h.InputSchema):
		return "InputSchema JSON"
	case mustJSON(t, p.OutputSchema) != mustJSON(t, h.OutputSchema):
		return "OutputSchema JSON"
	case (p.PrepareArguments != nil) != (h.PrepareArguments != nil):
		return "PrepareArguments presence"
	}
	return ""
}

// TS-13-36: proxy tools over local match tools.All one for one in order,
// with equal definition fields, byte-identical schemas and a remote-only
// execution. 13-REQ-6.1, 13-REQ-6.4.
func TestProxiesMatchToolsAll_TS13_36(t *testing.T) {
	isolate(t)
	sb := newLocal(t)
	if _, err := sb.Sync(context.Background(), sandbox.Tar{R: bytes.NewReader(tarOf(t, file("a.txt", "a"), file("m.go", "package m\n")))}); err != nil {
		t.Fatal(err)
	}
	proxies, err := sb.Tools(context.Background(), sandbox.ToolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ws, _ := tools.NewWorkspace(t.TempDir())
	host, err := tools.All(tools.Options{Workspace: ws, Ignore: tools.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	if len(proxies) != len(host) {
		t.Fatalf("%d proxies, %d tools.All tools", len(proxies), len(host))
	}
	for i, p := range proxies {
		h := host[i]
		if d := sameDefinition(t, p, h); d != "" {
			t.Errorf("%s (position %d) differs from tools.All's %s in %s", p.Name, i, h.Name, d)
		}
		if (p.PrepareArguments != nil) != (p.Name == "search_files" || p.Name == "edit_file") {
			t.Errorf("%s: PrepareArguments set = %v", p.Name, p.PrepareArguments != nil)
		}
		if p.Handler != nil || p.ReachableTools != nil || p.Terminating || p.Execute == nil {
			t.Errorf("%s: Handler %v, ReachableTools %v, Terminating %v, Execute set %v",
				p.Name, p.Handler != nil, p.ReachableTools, p.Terminating, p.Execute != nil)
		}
	}
	if err := sb.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, p := range proxies {
		if r := p.Execute(context.Background(), []byte(`{}`)); r.Error != "sandbox_unavailable" {
			t.Errorf("%s after Close: %+v (a local closure ran?)", p.Name, r)
		}
	}
}

// TS-13-39: a second Tools call with equal options returns equivalent
// proxies, and one with different options fails with ErrToolsConfigured
// without reconfiguring the agent. 13-REQ-6.5, 13-REQ-6.6.
func TestToolsRepeat_TS13_39(t *testing.T) {
	isolate(t)
	sb := newLocal(t)
	ctx := context.Background()
	a, err := sb.Tools(ctx, sandbox.ToolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := sb.Tools(ctx, sandbox.ToolOptions{})
	if err != nil || len(a) != len(b) {
		t.Fatalf("second Tools: %d, %v", len(b), err)
	}
	for i := range a {
		if d := sameDefinition(t, a[i], b[i]); d != "" {
			t.Errorf("%s differs between calls in %s", a[i].Name, d)
		}
	}
	if c, err := sb.Tools(ctx, sandbox.ToolOptions{DisableSpill: true}); c != nil || !errors.Is(err, sandbox.ErrToolsConfigured) {
		t.Fatalf("different options: %d tools, %v", len(c), err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	for _, p := range a {
		if p.Name == "execute" {
			r := p.Execute(ctx, []byte(`{"command":"yes x | head -c 400000"}`))
			if r.Metadata == nil || r.Metadata.SpillPath == "" {
				t.Errorf("execute did not spill: the agent lost its original options (%+v)", r.Metadata)
			}
		}
	}
}

// TS-13-40: concurrent proxy calls each get their own result, are not
// serialised on the host, and are race-free (run under go test -race).
// 13-REQ-6.7.
func TestConcurrentProxies_TS13_40(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell commands")
	}
	isolate(t)
	sb := newLocal(t)
	var es []entry
	for i := range 16 {
		es = append(es, file(fmt.Sprintf("f%d.txt", i), fmt.Sprintf("<idx-%d>\n", i)))
	}
	if _, err := sb.Sync(context.Background(), sandbox.Tar{R: bytes.NewReader(tarOf(t, es...))}); err != nil {
		t.Fatal(err)
	}
	rf, ex := tool(t, sb, "read_file"), tool(t, sb, "execute")
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			r := rf.Execute(context.Background(), []byte(fmt.Sprintf(`{"path":"f%d.txt"}`, i%16)))
			if want := fmt.Sprintf("<idx-%d>", i%16); !strings.Contains(r.Text, want) {
				t.Errorf("read_file f%d.txt: %q lacks %s", i%16, r.Text, want)
			}
			e := ex.Execute(context.Background(), []byte(fmt.Sprintf(`{"command":"echo '<e-%d>'"}`, i)))
			if want := fmt.Sprintf("<e-%d>", i); !strings.Contains(e.Text, want) {
				t.Errorf("echo %d: %q lacks %s", i, e.Text, want)
			}
		})
	}
	wg.Wait()
	start := time.Now()
	for range 8 {
		wg.Go(func() { ex.Execute(context.Background(), []byte(`{"command":"sleep 1"}`)) })
	}
	wg.Wait()
	if d := time.Since(start); d >= 4*time.Second {
		t.Errorf("eight parallel sleep 1 calls took %v: calls are serialised", d)
	}
}

// TS-13-62: agentkit.New refuses proxy shell tools with a nil Guard exactly
// as it refuses local ones. 13-REQ-10.1.
func TestUnguardedProxyShell_TS13_62(t *testing.T) {
	isolate(t)
	sb := newLocal(t)
	proxies, err := sb.Tools(context.Background(), sandbox.ToolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cfg := func(ts []core.Tool, g core.BeforeToolCall) agentkit.Config {
		return agentkit.Config{Provider: faux.New(), Model: faux.Model().ID, Tools: ts, Guard: g}
	}
	if a, err := agentkit.New(cfg(proxies, nil)); a != nil || !errors.Is(err, core.ErrUnguardedExecute) {
		t.Errorf("all proxies, nil Guard: %v, %v", a, err)
	}
	if a, err := agentkit.New(cfg([]core.Tool{tool(t, sb, "run_command")}, nil)); a != nil || !errors.Is(err, core.ErrUnguardedExecute) {
		t.Errorf("proxy run_command, nil Guard: %v, %v", a, err)
	}
	allow := func(context.Context, core.BeforeToolCallContext) core.BeforeToolCallDecision {
		return core.BeforeToolCallDecision{}
	}
	if a, err := agentkit.New(cfg(proxies, allow)); a == nil || err != nil {
		t.Errorf("all proxies with a Guard: %v, %v", a, err)
	}
}

// TS-13-63: every file effect of a proxy tool lands in the sandbox
// workspace and nothing in the host working directory. 13-REQ-10.3.
func TestEffectsLandInTheSandbox_TS13_63(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell commands")
	}
	isolate(t)
	hostDir := t.TempDir()
	t.Chdir(hostDir)
	sb := newLocal(t)
	ctx := context.Background()
	for _, c := range []struct{ tool, args string }{
		{"write_file", `{"path":"w.txt","content":"a"}`},
		{"edit_file", `{"path":"w.txt","edits":[{"old_string":"a","new_string":"b"}]}`},
		{"execute", `{"command":"echo e > e.txt"}`},
		{"run_command", `{"argv":["sh","-c","echo r > r.txt"]}`},
	} {
		if r := tool(t, sb, c.tool).Execute(ctx, []byte(c.args)); !r.OK {
			t.Errorf("%s: %s %s", c.tool, r.Error, r.Detail)
		}
	}
	ws := sb.Agent().Workdir
	for _, f := range []string{"w.txt", "e.txt", "r.txt"} {
		if !exists(filepath.Join(ws, f)) {
			t.Errorf("%s is not in the sandbox workspace", f)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(ws, "w.txt")); string(b) != "b" {
		t.Errorf("w.txt holds %q, want the edit applied", b)
	}
	if ents, _ := os.ReadDir(hostDir); len(ents) != 0 {
		t.Errorf("the host working directory gained %v", ents)
	}
}
