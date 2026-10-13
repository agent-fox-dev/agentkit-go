package images

import (
	"bytes"
	"debug/buildinfo"
	"debug/elf"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
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
	if want := versionLine(expectedVersion(os.Getenv)); code != 0 || out != want {
		t.Errorf("--version: exit %d, %q (stderr %q), want %q", code, out, se, want)
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

// cgoStage is the base stage's COPY source stage and path for the agent.
func cgoStage(t *testing.T) (stage, src string) {
	t.Helper()
	base := find(realStages(t), "base")
	for _, a := range base.all("COPY") {
		if c := parseCopy(a); c.from != "" && c.dest == sandbox.AgentPath && len(c.srcs) == 1 {
			return c.from, c.srcs[0]
		}
	}
	t.Fatal("the base stage copies no agent")
	return "", ""
}

// TS-16-49: the cgo stage cross-compiles for the other supported
// architecture on the build platform. 16-REQ-1.4.
func TestBaseImageCrossArchBuild(t *testing.T) {
	engine, _ := requireImage(t, "AGENTKIT_SANDBOX_BASE_IMAGE")
	stage, src := cgoStage(t)
	other, machine := "arm64", elf.EM_AARCH64
	if runtime.GOARCH != "amd64" {
		other, machine = "amd64", elf.EM_X86_64
	}
	ref := "agentkit-imgtest-cgo-" + randomHex(8)
	t.Cleanup(func() { runEngine(t, engine, "rmi", "-f", ref) })
	cmd := []string{"build", "-f", filepath.Join(repoRoot(t), "sandbox/images/Containerfile"), "--target", stage,
		"--platform", "linux/" + other, "--build-arg", "AGENTKIT_VERSION=v9.8.7", "-t", ref, repoRoot(t)}
	if so, se, code := runEngine(t, engine, cmd...); code != 0 {
		t.Fatalf("cross build: exit %d\n%s%s", code, so, se)
	}
	id, se, code := runEngine(t, engine, "create", ref)
	if code != 0 {
		t.Fatalf("create: %s", se)
	}
	id = strings.TrimSpace(id)
	t.Cleanup(func() { runEngine(t, engine, "rm", "-f", id) })
	bin := filepath.Join(t.TempDir(), "agentkit-sandboxd")
	if _, se, code := runEngine(t, engine, "cp", id+":"+src, bin); code != 0 {
		t.Fatalf("cp: %s", se)
	}
	f, err := elf.Open(bin)
	if err != nil {
		t.Fatal(err)
	}
	if f.Machine != machine {
		t.Errorf("machine %v, want %v", f.Machine, machine)
	}
	f.Close()
	bi, err := buildinfo.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	settings := map[string]string{}
	for _, s := range bi.Settings {
		settings[s.Key] = s.Value
	}
	for k, v := range map[string]string{"GOOS": "linux", "GOARCH": other, "CGO_ENABLED": "1", "-trimpath": "true"} {
		if settings[k] != v {
			t.Errorf("build setting %s = %q, want %q", k, settings[k], v)
		}
	}
	// With -trimpath, Go leaves -ldflags out of the build info
	// (go.dev/issue/52372), so the stamp is found in the binary itself
	// (docs/errata/16_sandbox_images.md).
	if ld, ok := settings["-ldflags"]; ok {
		if !strings.Contains(ld, "buildver.stamp=v9.8.7") {
			t.Errorf("-ldflags %q lacks the stamp", ld)
		}
	} else if b, err := os.ReadFile(bin); err != nil || !bytes.Contains(b, []byte("v9.8.7")) {
		t.Errorf("the binary does not carry the stamp v9.8.7 (%v)", err)
	}
}

// TS-16-50: a build for an unsupported architecture fails with a message
// naming the two supported platforms. 16-REQ-1.5.
func TestBaseImageUnsupportedArch(t *testing.T) {
	engine, _ := requireImage(t, "AGENTKIT_SANDBOX_BASE_IMAGE")
	stage, _ := cgoStage(t)
	for _, target := range []string{"overlay", stage} {
		ref := "agentkit-imgtest-bad-" + randomHex(8)
		so, se, code := runEngine(t, engine, "build", "-f", filepath.Join(repoRoot(t), "sandbox/images/Containerfile"), "--target", target,
			"--platform", "linux/riscv64", "-t", ref, repoRoot(t))
		out := so + se
		if code == 0 || !strings.Contains(out, "linux/amd64") || !strings.Contains(out, "linux/arm64") {
			t.Errorf("--target %s for linux/riscv64: exit %d\n%s", target, code, out)
		}
		if _, _, code := runEngine(t, engine, "image", "inspect", ref); code == 0 {
			t.Errorf("--target %s produced the image %s", target, ref)
			runEngine(t, engine, "rmi", "-f", ref)
		}
	}
}
