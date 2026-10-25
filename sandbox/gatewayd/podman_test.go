package gatewayd_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/podman"
)

// End to end over a real engine (skipped under -short, with no engine or no AGENTKIT_SANDBOX_BASE_IMAGE): a container
// sandbox clones a repository, runs the tools with no network, and leaves no labelled container or volume.
func TestAContainerSandboxBehindTheGatewayWorksAndLeavesNothing(t *testing.T) {
	engine, image := os.Getenv("AGENTKIT_CONTAINER"), os.Getenv("AGENTKIT_SANDBOX_BASE_IMAGE")
	for _, e := range []string{"podman", "docker"} {
		if _, err := exec.LookPath(e); engine == "" && err == nil {
			engine = e
		}
	}
	if _, err := exec.LookPath(engine); testing.Short() || err != nil || image == "" {
		t.Skipf("needs no -short, an engine (%q) and AGENTKIT_SANDBOX_BASE_IMAGE; run make sandbox-gateway-test", engine)
	}
	be, err := podman.New(podman.Options{Binary: engine})
	must(t, err)
	env := gwtest.Start(t, gatewayd.Config{Backend: be})
	p := env.Dial(t, gwtest.Token)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	sb, err := p.Create(ctx, sandbox.Spec{Image: image})
	must(t, err)
	left, err := p.Create(ctx, sandbox.Spec{Image: image}) // left to Shutdown
	must(t, err)
	url := os.Getenv("AGENTKIT_SANDBOX_GIT_URL")
	if url == "" {
		url = "https://github.com/agent-fox-dev/agentkit-go"
	}
	if _, err := sb.Sync(ctx, sandbox.GitRepo{URL: url, Depth: 1}); err != nil {
		t.Fatalf("GitRepo Sync: %v", err)
	}
	if r := run(t, sb, "read_file", `{"path":"go.mod"}`); !r.OK {
		t.Errorf("read_file of the clone: %+v", r)
	}
	if r := run(t, sb, "execute", `{"command":"exec 3<>/dev/tcp/1.1.1.1/443 && echo connected"}`); !strings.Contains(r.Text, "[exit ") {
		t.Errorf("TCP from execute did not fail under NetworkNone: %+v", r)
	}
	if err := sb.Close(ctx); err != nil || env.Server.Shutdown(ctx) != nil {
		t.Fatalf("Close = %v, or Shutdown failed", err)
	}
	for _, id := range []string{sb.ID(), left.ID()} {
		for _, args := range [][]string{{"ps", "-a"}, {"volume", "ls"}} {
			b, err := exec.Command(engine, append(args, "--filter", "label=agentkit.sandbox="+id, "-q")...).CombinedOutput()
			if err != nil || strings.TrimSpace(string(b)) != "" {
				t.Errorf("%s %v for sandbox %s: %q, %v; want nothing left", engine, args, id, b, err)
			}
		}
	}
}
