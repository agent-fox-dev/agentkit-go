package images

import (
	"os"
	"slices"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// TS-16-45: the overlay image has one layer, the agent entrypoint, and a
// static agent that runs --version with no libc. 16-REQ-3.1, 16-REQ-3.2,
// 16-REQ-1.3.
func TestOverlayImage(t *testing.T) {
	engine, overlay := requireImage(t, "AGENTKIT_SANDBOX_OVERLAY_IMAGE")
	var layers []string
	inspectJSON(t, engine, overlay, ".RootFS.Layers", &layers)
	if len(layers) != 1 {
		t.Errorf("RootFS.Layers %v, want exactly one", layers)
	}
	var ep []string
	inspectJSON(t, engine, overlay, ".Config.Entrypoint", &ep)
	if !slices.Equal(ep, []string{sandbox.AgentPath}) {
		t.Errorf("Entrypoint %v", ep)
	}
	out, se, code := runEngine(t, engine, "run", "--rm", "--entrypoint", sandbox.AgentPath, overlay, "--version")
	if want := expectedVersion(os.Getenv); code != 0 || !isVersionLine(out, want) {
		t.Errorf("--version: exit %d, %q (stderr %q), want version %q", code, out, se, want)
	}
}

// TS-16-46: an image of FROM base plus OverlayCopyLine runs the copied
// static agent, which passes the digest check and the Landlock check.
// 16-REQ-3.3, 16-REQ-3.4, 16-REQ-3.1.
func TestOverlayImageDerived(t *testing.T) {
	engine, overlay := requireImage(t, "AGENTKIT_SANDBOX_OVERLAY_IMAGE")
	base := os.Getenv("AGENTKIT_SANDBOX_BASE_IMAGE")
	if base == "" {
		t.Skip("AGENTKIT_SANDBOX_BASE_IMAGE is not set" + skipHint)
	}
	ref, _ := buildDerived(t, engine, base, overlay)
	h := startT(t, engine, ref)
	info := h.sb.Agent()
	if info.OutlineBackend != "none" {
		t.Errorf("OutlineBackend %q, want none (the static agent)", info.OutlineBackend)
	}
	if err := checkFacts(info, expectedVersion(os.Getenv)); err != nil {
		t.Error(err)
	}
	if rc, out := mustExec(t, h, "echo ok"); rc != 0 || out != "ok" {
		t.Errorf("echo ok: rc %d, %q", rc, out)
	}
	if rc, out := mustExec(t, h, "stat -c '%u %a' "+sandbox.AgentPath); rc != 0 || out != "0 755" {
		t.Errorf("the copied agent: rc %d, %q", rc, out)
	}
	if err := landlockCheck(info, execVia(h), t.Logf); err != nil {
		t.Error(err)
	}
}
