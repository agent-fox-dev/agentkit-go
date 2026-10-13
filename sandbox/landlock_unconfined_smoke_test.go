//go:build unix

package sandbox_test

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
)

const (
	unconfined15Marker = "AGENTKIT_SANDBOX_LOCAL_LANDLOCK_CHILD"
	unconfined15Done   = "LOCAL-LANDLOCK-CHILD-COMPLETE"
)

// noNewPrivs reads PR_GET_NO_NEW_PRIVS; nil off Linux.
var noNewPrivs func() int

// TS-15-45: a re-executed child runs a local sandbox end to end and
// afterwards can still write outside the workspace and connect on loopback.
// 15-PATH-3, 15-REQ-7.4, 15-REQ-4.7, 15-REQ-6.4.
func TestSmokeLocalUnconfined_TS15_45(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestSmokeLocalUnconfinedChild_TS15_45$", "-test.count=1", "-test.v")
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "TMPDIR=") && !strings.HasPrefix(kv, unconfined15Marker+"=") {
			env = append(env, kv)
		}
	}
	cmd.Env = append(env, unconfined15Marker+"=1", "TMPDIR="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), unconfined15Done) {
		t.Fatalf("the child: %v\n%s", err, out)
	}
}

// TestSmokeLocalUnconfinedChild_TS15_45 is TS-15-45's child role; without
// the marker it does nothing.
func TestSmokeLocalUnconfinedChild_TS15_45(t *testing.T) {
	if os.Getenv(unconfined15Marker) != "1" {
		t.Skip("runs only as TS-15-45's re-executed child")
	}
	fail := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		fmt.Fprintln(os.Stderr, "local landlock child: "+msg)
		t.Error(msg)
	}
	nnp := -1
	if noNewPrivs != nil {
		nnp = noNewPrivs()
	}
	ctx := context.Background()
	root := filepath.Join(os.TempDir(), "agentkit-sandbox-local")
	sb, err := local.Unisolated().Create(ctx, sandbox.Spec{Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted}, RequireLandlock: true})
	if sb != nil || err == nil || !strings.Contains(err.Error(), "RequireLandlock") || !errors.Is(err, sandbox.ErrUnsupported) {
		fail("Create with RequireLandlock = %v, %v", sb, err)
	}
	if ents, err := os.ReadDir(root); err == nil && len(ents) > 0 {
		fail("%s has %d entries after the refused Create", root, len(ents))
	}
	sb, err = local.Unisolated().Create(ctx, sandbox.Spec{Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted}})
	if err != nil {
		t.Fatal(err)
	}
	if l := sb.Agent().Landlock; l.ABI != 0 || l.Note != "in-process agent (local provider): no confinement" {
		fail("Agent().Landlock = %+v", l)
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "a.txt", Mode: 0o644, Size: 1})
	_, _ = tw.Write([]byte("a"))
	_ = tw.Close()
	if _, err := sb.Sync(ctx, sandbox.Tar{R: &buf}); err != nil {
		fail("Sync: %v", err)
	}
	proxies, err := sb.Tools(ctx, sandbox.ToolOptions{})
	if err != nil {
		fail("Tools: %v", err)
	}
	for _, p := range proxies {
		if p.Name == "execute" {
			if r := p.Execute(ctx, []byte(`{"command":"echo ok"}`)); !strings.Contains(r.Text, "ok") {
				fail("execute echo ok: %+v", r)
			}
		}
	}
	if err := sb.Close(ctx); err != nil {
		fail("Close: %v", err)
	}
	dir, err := os.MkdirTemp("", "unconfined15-")
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, "probe"), []byte("p"), 0o644)
		_ = os.RemoveAll(dir)
	}
	if err != nil {
		fail("creating a file outside the workspace: %v", err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fail("listen: %v", err)
	} else {
		go func() {
			if c, err := l.Accept(); err == nil {
				_ = c.Close()
			}
		}()
		if c, err := net.Dial("tcp", l.Addr().String()); err != nil {
			fail("loopback dial: %v", err)
		} else {
			_ = c.Close()
		}
		_ = l.Close()
	}
	if noNewPrivs != nil && noNewPrivs() != nnp {
		fail("PR_GET_NO_NEW_PRIVS changed from %d to %d", nnp, noNewPrivs())
	}
	if !t.Failed() {
		fmt.Println(unconfined15Done)
	}
}
