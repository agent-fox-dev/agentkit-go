package attach

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/agent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/fakeagent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// emptyTar is a tar stream with no entries.
var emptyTar = make([]byte, 1024)

// hermetic is the host's ignore environment in these tests.
var hermetic = workspace.NoGlobalExcludes()

// attachFake serves f and attaches a sandbox to it with cfg; the TTL
// defaults to an hour.
func attachFake(t *testing.T, f *fakeagent.Agent, cfg Config) sandbox.Sandbox {
	t.Helper()
	if cfg.Spec.TTL == 0 {
		cfg.Spec.TTL = time.Hour
	}
	sb, err := Dial(context.Background(), f.Serve(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sb.Close(context.Background()); f.Stop() })
	return sb
}

// fakeOf is a fake agent answering Hello with protocol 1.minor.
func fakeOf(minor uint32) *fakeagent.Agent {
	f := fakeagent.New()
	f.SetHello(&sandboxpb.HelloResponse{ProtocolMajor: 1, ProtocolMinor: minor, Version: "v1", Workdir: "/ws"})
	return f
}

// real attaches a sandbox to a real agent over a fresh workspace.
func real(t *testing.T) (sandbox.Sandbox, string) {
	t.Helper()
	ws := t.TempDir()
	a, err := agent.New(agent.Config{Workdir: ws, Env: []string{"PATH=" + os.Getenv("PATH"), "TMPDIR=" + t.TempDir()}, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	hostEnd, agentEnd := net.Pipe()
	go func() { _ = a.Serve(agentEnd) }()
	sb, err := Dial(context.Background(), hostEnd, Config{Spec: sandbox.Spec{TTL: time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sb.Close(context.Background()) })
	ws, _ = filepath.EvalSymlinks(ws)
	return sb, ws
}

// proxies returns sb's proxy tools by name.
func proxies(t *testing.T, sb sandbox.Sandbox) map[string]core.Tool {
	t.Helper()
	tl, err := sb.Tools(context.Background(), sandbox.ToolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]core.Tool{}
	for _, p := range tl {
		out[p.Name] = p
	}
	return out
}

// isLive fails t unless a proxy call on sb still reaches its agent.
func isLive(t *testing.T, sb sandbox.Sandbox) {
	t.Helper()
	if r := proxies(t, sb)["list_files"].Execute(context.Background(), []byte(`{"path":"."}`)); r.Error == "sandbox_unavailable" {
		t.Errorf("the sandbox did not stay live: %+v", r)
	}
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func read(p string) string { b, _ := os.ReadFile(p); return string(b) }
