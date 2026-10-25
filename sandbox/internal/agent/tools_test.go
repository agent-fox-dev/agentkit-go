package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/tooldigest"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// Tools is tools.For over the resolved root, with the agent's explicit
// environment (its TMPDIR for spills too) and no global git excludes, named
// and digested in order.
func TestToolsBuildsTheToolsWithTheExplicitEnvironment(t *testing.T) {
	cfg := testutil.Tree(t, map[string]string{"git/ignore": "secret.txt\n"})
	testutil.WriteTree(t, cfg, map[string]string{"gitconfig": "[core]\n\texcludesFile = " + filepath.Join(cfg, "git", "ignore") + "\n"})
	t.Setenv("AGENTKIT_HOST_ONLY", "1")
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(cfg, "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	ws := testutil.Tree(t, map[string]string{"secret.txt": "s", "ignored.txt": "i", ".gitignore": "ignored.txt\n"})
	spill := t.TempDir()
	t.Setenv("TMPDIR", t.TempDir())
	env := []string{"PATH=" + os.Getenv("PATH"), "TMPDIR=" + spill, "AGENT_ONLY=1"}
	c, _ := serveRaw(t, newAgent(t, ws, env, time.Hour))
	resp, err := c.Tools(context.Background(), &sandboxpb.ToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	w, _ := workspace.Open(ws, workspace.Options{Ignore: workspace.NoGlobalExcludes()})
	want, _ := tools.For(w, tools.Options{Env: env})
	if len(resp.Tools) != len(want) {
		t.Fatalf("%d tools, want %d", len(resp.Tools), len(want))
	}
	for i, ti := range resp.Tools {
		if ti.Name != want[i].Name || string(ti.Digest) != string(tooldigest.Digest(want[i])) {
			t.Errorf("tool %d: %s, want %s with the shared digest", i, ti.Name, want[i].Name)
		}
	}
	ff := call(t, c, "find_files", `{"pattern":"*.txt"}`)
	if got := ff.Text + string(ff.Data); !ff.Ok || !strings.Contains(got, "secret.txt") || strings.Contains(got, "ignored.txt") {
		t.Errorf("find_files: %q %q; want secret.txt, which only the global excludes name", ff.Text, ff.Data)
	}
	out := call(t, c, "execute", `{"command":"env"}`)
	if !strings.Contains(out.Text, "AGENT_ONLY=1") || strings.Contains(out.Text, "AGENTKIT_HOST_ONLY") {
		t.Errorf("execute env:\n%s", out.Text)
	}
	var md core.ToolMetadata
	r := call(t, c, "execute", `{"command":"yes x | head -c 400000"}`)
	if err := json.Unmarshal(r.Metadata, &md); err != nil || !strings.HasPrefix(md.SpillPath, filepath.Join(spill, "agentkit-spill")) {
		t.Errorf("SpillPath %q, %v; want it under %s", md.SpillPath, err, spill)
	}
}

// A repeat with the same options returns the first answer; one with other
// options, or a relative spill directory, is refused and changes nothing.
func TestToolsIsConfiguredOnce(t *testing.T) {
	ws := testutil.Tree(t, map[string]string{"f.txt": "hello\n"})
	c, _ := serveRaw(t, newAgent(t, ws, testEnv(t), time.Hour))
	ctx := context.Background()
	if _, err := c.Tools(ctx, &sandboxpb.ToolsRequest{SpillDir: "rel/dir"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("relative spill_dir: %v, want InvalidArgument", err)
	}
	if r := call(t, c, "read_file", `{"path":"f.txt"}`); r.Error != "unknown_tool" {
		t.Fatalf("after the refusal a call got %q, want unknown_tool", r.Error)
	}
	a, err := c.Tools(ctx, &sandboxpb.ToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if b, err := c.Tools(ctx, &sandboxpb.ToolsRequest{}); err != nil || !proto.Equal(a, b) {
		t.Fatalf("a repeated Tools answered %v, %v; want the first answer", b, err)
	}
	if _, err := c.Tools(ctx, &sandboxpb.ToolsRequest{SymbolsMaxFiles: 7}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("differing options: %v, want FailedPrecondition", err)
	}
	if r := call(t, c, "read_file", `{"path":"f.txt"}`); !r.Ok {
		t.Fatalf("read_file after the refusal: %s %s", r.Error, r.Detail)
	}
}

func TestToolsReportsABuildFailureAsInternal(t *testing.T) {
	a := newAgent(t, "", testEnv(t), time.Hour)
	a.build = func(*workspace.Workspace, tools.Options) ([]core.Tool, error) { return nil, errors.New("no tools") }
	c, _ := serveRaw(t, a)
	if _, err := c.Tools(context.Background(), &sandboxpb.ToolsRequest{}); status.Code(err) != codes.Internal {
		t.Errorf("Tools = %v, want Internal", err)
	}
}

// Under a ruleset a spill directory must lie beneath a writable directory
// root; the default spill directory is never checked.
func TestToolsRefusesASpillDirOutsideTheWritableLandlockRoots(t *testing.T) {
	base := t.TempDir()
	w, tmp, o := filepath.Join(base, "w"), filepath.Join(base, "t"), filepath.Join(base, "o")
	testutil.WriteTree(t, base, map[string]string{"w/.keep": "", "t/.keep": "", "o/.keep": ""})
	toolsWith := func(l sandbox.LandlockInfo, spill string) error {
		a, err := New(Config{Workdir: w, Env: []string{"TMPDIR=" + tmp}, TTL: time.Hour, Landlock: l})
		if err != nil {
			t.Fatal(err)
		}
		c, _ := serveRaw(t, a)
		_, err = c.Tools(context.Background(), &sandboxpb.ToolsRequest{SpillDir: spill})
		return err
	}
	confined := sandbox.LandlockInfo{ABI: 4, TCP: true, Writable: []string{w, tmp, "/dev/null"}}
	for _, spill := range []string{o, w + "-evil", "/dev/null/spill"} {
		err := toolsWith(confined, spill)
		msg := status.Convert(err).Message()
		if status.Code(err) != codes.InvalidArgument || !strings.Contains(msg, spill) || !strings.Contains(msg, w) || !strings.Contains(msg, tmp) {
			t.Errorf("SpillDir %s: %v, want InvalidArgument naming it, %s and %s", spill, err, w, tmp)
		}
	}
	for name, c := range map[string]struct {
		l     sandbox.LandlockInfo
		spill string
	}{
		"default":            {confined, ""},
		"beneath a root":     {confined, filepath.Join(w, "spill")},
		"a root itself":      {confined, tmp},
		"tmp not writable":   {sandbox.LandlockInfo{ABI: 4, Writable: []string{w}}, ""},
		"no ruleset applied": {sandbox.LandlockInfo{}, o},
	} {
		if err := toolsWith(c.l, c.spill); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
