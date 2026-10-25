package images

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/attach"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/stdioconn"
)

// The tests of the published sandbox image are opt-in: make
// build-containers builds the images and the variables name them.

const skipHint = "; run make build-containers and set AGENTKIT_SANDBOX_BASE_IMAGE to run these tests"

// baseImage skips t under -short, without a container engine
// (AGENTKIT_CONTAINER when set, else podman, else docker) or without
// AGENTKIT_SANDBOX_BASE_IMAGE, and returns the engine and the image.
func baseImage(t *testing.T) (engine, image string) {
	t.Helper()
	candidates := []string{"podman", "docker"}
	if e := os.Getenv("AGENTKIT_CONTAINER"); e != "" {
		candidates = []string{e}
	}
	for _, e := range candidates {
		if _, err := exec.LookPath(e); err == nil && engine == "" {
			engine = e
		}
	}
	image = os.Getenv("AGENTKIT_SANDBOX_BASE_IMAGE")
	switch {
	case testing.Short():
		t.Skip("image tests are skipped under -short" + skipHint)
	case engine == "":
		t.Skipf("no container engine %v found%s", candidates, skipHint)
	case image == "":
		t.Skip("AGENTKIT_SANDBOX_BASE_IMAGE is not set" + skipHint)
	}
	return engine, image
}

// expectedVersion is the version the agent must report: the stamped one,
// or (devel) for an unstamped build.
func expectedVersion() string {
	if v := os.Getenv("AGENTKIT_SANDBOX_IMAGE_VERSION"); v != "" {
		return v
	}
	return "(devel)"
}

// agent is the image's agent started on the engine's stdio, and the
// sandbox attached to it.
type agent struct {
	sb     sandbox.Sandbox
	tools  []core.Tool
	stderr bytes.Buffer
}

// startAgent runs image's agent as the image's own user, with no network,
// attaches to it, and removes the container when t ends.
func startAgent(t *testing.T, engine, image string) *agent {
	t.Helper()
	spec := sandbox.Spec{Network: sandbox.Network{Mode: sandbox.NetworkNone}, TTL: 10 * time.Minute, Workdir: "/workspace"}
	name := fmt.Sprintf("agentkit-imgtest-%x", time.Now().UnixNano())
	a := &agent{}
	cmd := exec.Command(engine, append([]string{"run", "--rm", "-i", "--name", name, "--network", "none",
		"--env", "HOME=/home/agentkit", "--env", "TMPDIR=/tmp", "--entrypoint", sandbox.AgentPath, image}, sandbox.AgentArgs(spec)...)...)
	cmd.Stderr = &a.stderr
	stdin, err1 := cmd.StdinPipe()
	stdout, err2 := cmd.StdoutPipe()
	if err1 != nil || err2 != nil || cmd.Start() != nil {
		t.Fatalf("starting %s: %v %v", image, err1, err2)
	}
	t.Cleanup(func() {
		if a.sb != nil {
			_ = a.sb.Close(context.Background())
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = exec.Command(engine, "rm", "-f", name).Run()
		if t.Failed() {
			t.Logf("engine stderr: %s", a.stderr.String())
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if a.sb, err1 = attach.Dial(ctx, stdioconn.New(stdout, stdin), attach.Config{ID: name, Spec: spec, Provider: "imgtest"}); err1 != nil {
		t.Fatalf("attaching to %s: %v", image, err1)
	}
	if a.tools, err1 = a.sb.Tools(ctx, sandbox.ToolOptions{}); err1 != nil || len(a.tools) == 0 {
		t.Fatalf("Tools = %d tools, %v", len(a.tools), err1)
	}
	return a
}

var rcLine = regexp.MustCompile(`(?m)^rc=(\d+)\s*$`)

// exec runs command through the agent's execute tool and returns its exit
// code and its output, trimmed.
func (a *agent) exec(t *testing.T, command string) (int, string) {
	t.Helper()
	r := a.call(t, "execute", map[string]string{"command": command + "; echo rc=$?"})
	m := rcLine.FindAllStringSubmatchIndex(r.Text, -1)
	if r.Error != "" || len(m) == 0 {
		t.Fatalf("execute %q: %s %s %q", command, r.Error, r.Detail, r.Text)
	}
	last := m[len(m)-1]
	rc, _ := strconv.Atoi(r.Text[last[2]:last[3]])
	return rc, strings.TrimSpace(r.Text[:last[0]])
}

func (a *agent) call(t *testing.T, name string, input any) core.ToolResult {
	t.Helper()
	b, _ := json.Marshal(input)
	for _, tl := range a.tools {
		if tl.Name == name {
			return tl.Execute(context.Background(), b)
		}
	}
	t.Fatalf("no tool %s", name)
	return core.ToolResult{}
}

// inspect decodes `image inspect --format '{{json <format>}}'` of image.
func inspect(t *testing.T, engine, image, format string, v any) {
	t.Helper()
	out, err := exec.Command(engine, "image", "inspect", "--format", "{{json "+format+"}}", image).Output()
	if err != nil || json.Unmarshal(bytes.TrimSpace(out), v) != nil {
		t.Fatalf("image inspect %s %s: %v\n%s", image, format, err, out)
	}
}
