package gatewayd_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/podman"
)

// The opt-in Podman tests run the gateway over a real podman or docker and
// the locally built base image. make sandbox-gateway-test builds the images
// and sets the variables.

const realHint = "; run make sandbox-gateway-test to build the images and run these tests"

// realEngine skips t unless the prerequisites hold, as specs 16 and 17
// decide them: not under -short, an engine (AGENTKIT_CONTAINER when set,
// then podman, then docker), and AGENTKIT_SANDBOX_BASE_IMAGE set. It returns
// the engine and the image.
func realEngine(t *testing.T) (engine, image string) {
	t.Helper()
	if testing.Short() {
		t.Skip("the gateway's Podman tests are skipped under -short" + realHint)
	}
	if e := os.Getenv("AGENTKIT_CONTAINER"); e != "" {
		if _, err := exec.LookPath(e); err != nil {
			t.Skipf("AGENTKIT_CONTAINER=%s is not on PATH%s", e, realHint)
		}
		engine = e
	} else {
		for _, e := range []string{"podman", "docker"} {
			if _, err := exec.LookPath(e); err == nil {
				engine = e
				break
			}
		}
		if engine == "" {
			t.Skip("no container engine (podman or docker) found" + realHint)
		}
	}
	if image = os.Getenv("AGENTKIT_SANDBOX_BASE_IMAGE"); image == "" {
		t.Skip("AGENTKIT_SANDBOX_BASE_IMAGE is not set" + realHint)
	}
	return engine, image
}

// podmanGateway serves a gateway over podman.New with the ceiling [none].
func podmanGateway(t *testing.T, engine string) *gwtest.Env {
	t.Helper()
	be, err := podman.New(podman.Options{Binary: engine})
	if err != nil {
		t.Fatal(err)
	}
	return gwtest.Start(t, gatewayd.Config{Backend: be})
}

// engineLabelled is what the engine still lists with the sandbox's label:
// containers, then volumes.
func engineLabelled(t *testing.T, engine, id string) string {
	t.Helper()
	var out strings.Builder
	for _, args := range [][]string{
		{"ps", "-a", "--filter", "label=agentkit.sandbox=" + id, "-q"},
		{"volume", "ls", "--filter", "label=agentkit.sandbox=" + id, "-q"},
	} {
		b, err := exec.Command(engine, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v: %s", engine, args, err, b)
		}
		out.Write(b)
	}
	return strings.TrimSpace(out.String())
}

// TS-19-71: Shutdown of a gateway over Podman destroys the live sandboxes
// and leaves no labelled container or volume. 19-REQ-10.5, 19-REQ-6.8.
func TestPodmanShutdown_TS19_71(t *testing.T) {
	engine, image := realEngine(t)
	env := podmanGateway(t, engine)
	p := env.Dial(t, gwtest.Token)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var ids []string
	for range 2 {
		sb, err := p.Create(ctx, sandbox.Spec{Image: image})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, sb.ID())
	}
	if err := env.Server.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown = %v", err)
	}
	for _, id := range ids {
		if left := engineLabelled(t, engine, id); left != "" {
			t.Errorf("the engine still lists %s for sandbox %s", left, id)
		}
	}
}

// gitURL is the public repository the opt-in GitRepo test clones.
func gitURL() string {
	if u := os.Getenv("AGENTKIT_SANDBOX_GIT_URL"); u != "" {
		return u
	}
	return "https://github.com/agent-fox-dev/agentkit-go"
}

// TS-19-77 (smoke, 19-PATH-6): a Podman sandbox behind the gateway clones a
// public repository, runs the proxies under NetworkNone and leaves nothing
// behind. 19-REQ-10.5.
func TestPodmanSmoke_TS19_77(t *testing.T) {
	engine, image := realEngine(t)
	env := podmanGateway(t, engine)
	p := env.Dial(t, gwtest.Token)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	sb, err := p.Create(ctx, sandbox.Spec{Image: image})
	if err != nil {
		t.Fatal(err)
	}
	id := sb.ID()
	if _, err := sb.Sync(ctx, sandbox.GitRepo{URL: gitURL(), Depth: 1}); err != nil {
		t.Fatalf("GitRepo Sync: %v", err)
	}
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "synced.txt"), []byte("synced\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := sb.Sync(ctx, sandbox.LocalDir{Path: src}); err != nil {
		t.Fatalf("LocalDir Sync: %v", err)
	}
	tl, err := sb.Tools(ctx, sandbox.ToolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]core.Tool{}
	for _, x := range tl {
		byName[x.Name] = x
	}
	call := func(name, in string) core.ToolResult { return byName[name].Execute(ctx, []byte(in)) }
	for _, path := range []string{"go.mod", "synced.txt"} {
		if r := call("read_file", `{"path":"`+path+`"}`); !r.OK {
			t.Errorf("read_file %s: %+v", path, r)
		}
	}
	if r := call("write_file", `{"path":"written.txt","content":"written\n"}`); !r.OK {
		t.Errorf("write_file: %+v", r)
	}
	if r := call("execute", `{"command":"cat written.txt"}`); !r.OK || !strings.Contains(r.Text+fmt.Sprint(r.Data), "written") {
		t.Errorf("execute: %+v", r)
	}
	if r := call("execute", `{"command":"exec 3<>/dev/tcp/1.1.1.1/443 && echo connected"}`); r.OK && strings.Contains(r.Text+fmt.Sprint(r.Data), "connected") {
		t.Errorf("TCP from execute succeeded under NetworkNone: %+v", r)
	}
	out := filepath.Join(t.TempDir(), "out")
	if err := sb.Export(ctx, ".", sandbox.LocalDir{Path: out}); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(out, "written.txt")); err != nil || string(b) != "written\n" {
		t.Errorf("the export's written.txt: %q, %v", b, err)
	}
	if err := sb.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if left := engineLabelled(t, engine, id); left != "" {
		t.Errorf("after Close the engine still lists %s", left)
	}
}
