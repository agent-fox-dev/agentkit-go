package agent

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

func hello(t *testing.T, cfg Config) *sandboxpb.HelloResponse {
	t.Helper()
	cfg.TTL = time.Hour
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := serveRaw(t, a)
	h, err := c.Hello(context.Background(), &sandboxpb.HelloRequest{})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// Hello reports the protocol, the module version, the resolved workspace
// root, the outline backend and the caller's Landlock ruleset.
func TestHelloReportsTheAgent(t *testing.T) {
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	l := sandbox.LandlockInfo{ABI: 3, KernelABI: 5, Note: "n", Writable: []string{"/w1", "/dev/null"}, ReadOnly: []string{"/usr"}}
	h := hello(t, Config{Workdir: link, Landlock: l})
	want, _ := filepath.EvalSymlinks(link)
	if h.ProtocolMajor != sandbox.ProtocolMajor || h.ProtocolMinor != sandbox.ProtocolMinor || h.Workdir != want {
		t.Errorf("Hello = %v, want protocol %d.%d and workdir %s", h, sandbox.ProtocolMajor, sandbox.ProtocolMinor, want)
	}
	version := "(devel)"
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" {
		version = bi.Main.Version
	}
	if h.Version != version || h.OutlineBackend != wantOutlineBackend {
		t.Errorf("Version %q, OutlineBackend %q; want %q and %q", h.Version, h.OutlineBackend, version, wantOutlineBackend)
	}
	if r := h.GetLandlock(); r.GetAbi() != 3 || r.GetKernelAbi() != 5 || r.GetTcp() || r.GetNote() != "n" ||
		!reflect.DeepEqual(r.GetWritable(), l.Writable) || !reflect.DeepEqual(r.GetReadOnly(), l.ReadOnly) {
		t.Errorf("HelloResponse.landlock = %v, want %+v", r, l)
	}
}

// Git is whether git is an executable on the explicit environment's PATH,
// never the process's.
func TestHelloLooksForGitOnTheExplicitPath(t *testing.T) {
	gitDir, plainDir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(gitDir, "git"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plainDir, "git"), []byte("not executable"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", gitDir)
	for path, want := range map[string]bool{gitDir: true, plainDir: false, "": false, "relative:" + plainDir: false} {
		if got := hello(t, Config{Workdir: t.TempDir(), Env: []string{"PATH=" + path}}).Git; got != want {
			t.Errorf("PATH=%s: Git = %v, want %v", path, got, want)
		}
	}
}
