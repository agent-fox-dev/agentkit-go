package agent

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/tooldigest"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

// testEnv is an explicit agent environment: the host's PATH and a private
// HOME and TMPDIR.
func testEnv(t *testing.T) []string {
	t.Helper()
	return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()}
}

// newAgent builds an agent over dir (a fresh temp dir when empty).
func newAgent(t *testing.T, dir string, env []string, ttl time.Duration) *Agent {
	t.Helper()
	if dir == "" {
		dir = t.TempDir()
	}
	a, err := New(Config{Workdir: dir, Env: env, TTL: ttl})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// serveRaw serves a over a net.Pipe and returns a raw client of it and the
// channel Serve's result arrives on.
func serveRaw(t *testing.T, a *Agent) (sandboxpb.AgentClient, <-chan error) {
	t.Helper()
	hostEnd, agentEnd := net.Pipe()
	errc := make(chan error, 1)
	go func() { errc <- a.Serve(agentEnd) }()
	cc, err := grpc.NewClient("passthrough:///agent",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return hostEnd, nil }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(64<<20), grpc.MaxCallSendMsgSize(64<<20)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cc.Close() })
	return sandboxpb.NewAgentClient(cc), errc
}

// call runs one tool call and fails the test on an RPC error.
func call(t *testing.T, c sandboxpb.AgentClient, name, input string) *sandboxpb.CallResponse {
	t.Helper()
	r, err := c.Call(context.Background(), &sandboxpb.CallRequest{Name: name, Input: []byte(input)})
	if err != nil {
		t.Fatalf("Call %s: %v", name, err)
	}
	return r
}

func writeFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TS-13-14: the agent refuses to start on a missing workspace path or one
// that is a regular file. 13-REQ-3.1.
func TestNewRefusesBadWorkspace_TS13_14(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	writeFile(t, dir, "file", "x")
	for _, p := range []string{filepath.Join(dir, "nope"), file} {
		a, err := New(Config{Workdir: p, Env: testEnv(t), TTL: time.Hour})
		if a != nil || err == nil || !strings.Contains(err.Error(), p) {
			t.Errorf("New(%s) = %v, %v; want nil and an error naming the path", p, a, err)
		}
	}
}

// expectedVersion is the main module's version from the build info, or
// (devel).
func expectedVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" {
		return bi.Main.Version
	}
	return "(devel)"
}

// TS-13-15: Hello reports the protocol version, the module version, the
// resolved workspace root, the outline backend and git presence.
// 13-REQ-3.2.
func TestHello_TS13_15(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	gitDir, emptyDir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(gitDir, "git"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	hello := func(dir, path string) *sandboxpb.HelloResponse {
		c, _ := serveRaw(t, newAgent(t, dir, []string{"PATH=" + path}, time.Hour))
		h, err := c.Hello(context.Background(), &sandboxpb.HelloRequest{})
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	h := hello(link, gitDir)
	want, _ := filepath.EvalSymlinks(link)
	if h.ProtocolMajor != 1 || h.ProtocolMinor != 2 || h.Workdir != want {
		t.Errorf("Hello = %v, want protocol 1.2 and workdir %s", h, want)
	}
	if h.Version != expectedVersion() {
		t.Errorf("Version %q, want %q", h.Version, expectedVersion())
	}
	if h.OutlineBackend != wantOutlineBackend {
		t.Errorf("OutlineBackend %q, want %q", h.OutlineBackend, wantOutlineBackend)
	}
	if !h.Git {
		t.Error("Git is false with git on the explicit PATH")
	}
	if hello(real, emptyDir).Git {
		t.Error("Git is true with no git on the explicit PATH")
	}
}

// TS-13-16: Tools builds tools.All with the explicit environment and
// NoGlobalExcludes and returns names and shared-function digests in
// tools.All order. 13-REQ-3.3.
func TestToolsExplicitEnv_TS13_16(t *testing.T) {
	cfg := t.TempDir()
	writeFile(t, cfg, "git/ignore", "secret.txt\n")
	t.Setenv("AGENTKIT_HOST_ONLY", "1")
	t.Setenv("XDG_CONFIG_HOME", cfg)
	// git's own core.excludesFile would take precedence over the XDG path;
	// point it at the same file so the global layer lists secret.txt on any
	// machine.
	writeFile(t, cfg, "gitconfig", "[core]\n\texcludesFile = "+filepath.Join(cfg, "git", "ignore")+"\n")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(cfg, "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	ws := t.TempDir()
	writeFile(t, ws, "secret.txt", "s")
	writeFile(t, ws, "ignored.txt", "i")
	writeFile(t, ws, ".gitignore", "ignored.txt\n")
	env := append(testEnv(t), "AGENT_ONLY=1")
	c, _ := serveRaw(t, newAgent(t, ws, env, time.Hour))

	resp, err := c.Tools(context.Background(), &sandboxpb.ToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	w, _ := tools.NewWorkspace(ws)
	want, err := tools.All(tools.Options{Workspace: w, Env: env, Ignore: tools.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Tools) != len(want) || len(want) != 11 {
		t.Fatalf("%d tools, want %d", len(resp.Tools), len(want))
	}
	for i, ti := range resp.Tools {
		if ti.Name != want[i].Name || string(ti.Digest) != string(tooldigest.Digest(want[i])) {
			t.Errorf("tool %d: %s, want %s with the shared digest", i, ti.Name, want[i].Name)
		}
	}
	// list_files does not apply .gitignore (its description says so), so
	// the ignore layers are checked through find_files, which applies both.
	ff := call(t, c, "find_files", `{"pattern":"*.txt"}`)
	if got := ff.Text + string(ff.Data); !ff.Ok || !strings.Contains(got, "secret.txt") || strings.Contains(got, "ignored.txt") {
		t.Errorf("find_files: %q %q", ff.Text, ff.Data)
	}
	out := call(t, c, "execute", `{"command":"env"}`)
	if !strings.Contains(out.Text, "AGENT_ONLY=1") || strings.Contains(out.Text, "AGENTKIT_HOST_ONLY") {
		t.Errorf("execute env:\n%s", out.Text)
	}
}

// TS-13-17: an empty SpillDir spills under the explicit environment's
// TMPDIR, not the process's os.TempDir. 13-REQ-3.3.
func TestSpillUnderExplicitTMPDIR_TS13_17(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	t.Setenv("TMPDIR", dirB)
	c, _ := serveRaw(t, newAgent(t, "", []string{"PATH=" + os.Getenv("PATH"), "TMPDIR=" + dirA}, time.Hour))
	if _, err := c.Tools(context.Background(), &sandboxpb.ToolsRequest{}); err != nil {
		t.Fatal(err)
	}
	r := call(t, c, "execute", `{"command":"yes x | head -c 400000"}`)
	var md core.ToolMetadata
	if err := json.Unmarshal(r.Metadata, &md); err != nil {
		t.Fatalf("metadata %q: %v", r.Metadata, err)
	}
	if !strings.HasPrefix(md.SpillPath, filepath.Join(dirA, "agentkit-spill")) {
		t.Errorf("SpillPath %q is not under %s", md.SpillPath, filepath.Join(dirA, "agentkit-spill"))
	}
	if ents, _ := os.ReadDir(dirB); len(ents) != 0 {
		t.Errorf("the process TMPDIR gained %v", ents)
	}
}

// TS-13-18: a relative SpillDir is InvalidArgument, differing repeated
// options are FailedPrecondition, and identical repeats return the same
// answer. 13-REQ-3.4.
func TestToolsRepeatAndRefusals_TS13_18(t *testing.T) {
	ws := t.TempDir()
	writeFile(t, ws, "f.txt", "hello\n")
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
	b, err := c.Tools(ctx, &sandboxpb.ToolsRequest{})
	if err != nil || !proto.Equal(a, b) {
		t.Fatalf("a repeated Tools answered %v, %v; want the first answer", b, err)
	}
	if _, err := c.Tools(ctx, &sandboxpb.ToolsRequest{SymbolsMaxFiles: 7}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("differing options: %v, want FailedPrecondition", err)
	}
	if r := call(t, c, "read_file", `{"path":"f.txt"}`); !r.Ok {
		t.Fatalf("read_file after the refusal: %s %s", r.Error, r.Detail)
	}
}
