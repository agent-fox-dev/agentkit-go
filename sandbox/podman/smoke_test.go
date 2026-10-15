package podman

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// The smoke tests run the five execution paths against a real engine and
// the locally built base image; they skip as the integration tests do.

// labelledAll is every container and volume the engine lists with the label
// key agentkit.sandbox.
func labelledAll(t *testing.T, engine string) []string {
	t.Helper()
	var all []string
	for _, args := range [][]string{
		{"ps", "-a", "--filter", "label=agentkit.sandbox", "--format", "{{.Names}}"},
		{"volume", "ls", "--filter", "label=agentkit.sandbox", "--format", "{{.Name}}"},
	} {
		b, err := exec.Command(engine, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v: %s", engine, args, err, b)
		}
		all = append(all, strings.Fields(string(b))...)
	}
	slices.Sort(all)
	return all
}

// hexID is n random bytes in hex.
func hexID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// TS-17-65: an embedder runs a model's tools in a real container and gets
// the edits back, leaving nothing behind. 17-PATH-1, 17-REQ-9.2,
// 17-REQ-6.5, 17-REQ-5.1.
func TestSmokeRoundTrip_TS17_65(t *testing.T) {
	engine, image := realEngine(t)
	src := t.TempDir()
	for name, body := range map[string]string{"src/main.go": "package main\n\nfunc main() {}\n", ".gitignore": "build/\n", "build/out.bin": "binary"} {
		p := filepath.Join(src, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p := newRealProvider(t, engine, Options{})
	ctx := context.Background()
	s := newRealSandbox(t, p, sandbox.Spec{Image: image, Resources: sandbox.Resources{MemoryBytes: 256 << 20}})
	if _, err := s.Sync(ctx, sandbox.LocalDir{Path: src}); err != nil {
		t.Fatal(err)
	}
	ts := realTools(t, s)
	if r := callTool(t, ts, "read_file", map[string]any{"path": "src/main.go"}); !r.OK {
		t.Fatalf("read_file: %+v", r)
	}
	if r := callTool(t, ts, "write_file", map[string]any{"path": "new.txt", "content": "x"}); !r.OK {
		t.Fatalf("write_file: %+v", r)
	}
	if r := callTool(t, ts, "execute", map[string]any{"command": "ls"}); !r.OK || !strings.Contains(r.Text, "new.txt") {
		t.Fatalf("execute: %+v", r)
	}
	out := t.TempDir()
	if err := s.Export(ctx, ".", sandbox.LocalDir{Path: out}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "new.txt")); err != nil {
		t.Errorf("the export lacks new.txt: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "build", "out.bin")); err == nil {
		t.Error("the export holds build/out.bin, which .gitignore excludes")
	}
	id := s.ID()
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if left := engineLabelled(t, engine, id); left != "" {
		t.Errorf("after Close the engine still lists %s", left)
	}
}

// TS-17-66: a public repository is cloned through the init container while
// the sandbox keeps no network. 17-PATH-2, 17-REQ-9.4, 17-REQ-7.2,
// 17-REQ-7.3.
func TestSmokeGitRepo_TS17_66(t *testing.T) {
	engine, image := realEngine(t)
	url := os.Getenv("AGENTKIT_SANDBOX_GIT_URL")
	if url == "" {
		url = "https://github.com/agent-fox-dev/agentkit-go"
	}
	s := newRealSandbox(t, newRealProvider(t, engine, Options{}), sandbox.Spec{Image: image})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	r, err := s.Sync(ctx, sandbox.GitRepo{URL: url, Depth: 1})
	if err != nil || r != (sandbox.SyncResult{}) {
		t.Fatalf("GitRepo Sync of %s = %+v, %v", url, r, err)
	}
	ts := realTools(t, s)
	if res := callTool(t, ts, "read_file", map[string]any{"path": "go.mod"}); !res.OK || !strings.Contains(fmt.Sprint(res.Data["content"]), "module ") {
		t.Errorf("read_file go.mod of the clone: %+v", res)
	}
	if res := callTool(t, ts, "execute", map[string]any{"command": "bash -c 'exec 3<>/dev/tcp/1.1.1.1/443'"}); res.OK {
		t.Errorf("a TCP connect from the sandbox succeeded: %+v", res)
	}
	if _, err := s.Sync(ctx, sandbox.GitRepo{URL: url}); err == nil || !strings.Contains(err.Error(), "GitRepo must be the first Sync and come before Tools") {
		t.Errorf("second GitRepo Sync = %v, want the slot rule", err)
	}
	if b, _ := exec.Command(engine, "ps", "-a", "--filter", "name=agentkit-"+s.ID()+"-init", "-q").Output(); strings.TrimSpace(string(b)) != "" {
		t.Errorf("the init container remains: %s", b)
	}
}

// TS-17-67: Create with an image that has no agent reports ErrNoAgent with
// the fix and leaves nothing behind. 17-PATH-3, 17-REQ-5.5,
// 17-REQ-5.8.
func TestSmokeNoAgent_TS17_67(t *testing.T) {
	engine, image := realEngine(t)
	dir := t.TempDir()
	cf := "FROM " + image + "\nUSER root\nRUN rm " + sandbox.AgentPath + "\n"
	if err := os.WriteFile(filepath.Join(dir, "Containerfile"), []byte(cf), 0o644); err != nil {
		t.Fatal(err)
	}
	noAgent := "agentkit-podmantest-noagent-" + hexID(6)
	t.Cleanup(func() { _ = exec.Command(engine, "rmi", "-f", noAgent).Run() })
	if b, err := exec.Command(engine, "build", "-t", noAgent, "-f", filepath.Join(dir, "Containerfile"), dir).CombinedOutput(); err != nil {
		t.Fatalf("building the image without an agent: %v\n%s", err, b)
	}
	before := labelledAll(t, engine)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	s, err := newRealProvider(t, engine, Options{}).Create(ctx, sandbox.Spec{Image: noAgent})
	if s != nil {
		_ = s.Close(ctx)
		t.Fatal("Create returned a sandbox")
	}
	if !errors.Is(err, sandbox.ErrNoAgent) || !strings.Contains(err.Error(), sandbox.AgentPath) || !strings.Contains(err.Error(), "COPY --from=") {
		t.Fatalf("Create = %v, want ErrNoAgent with AgentPath and a COPY --from line", err)
	}
	if after := labelledAll(t, engine); !slices.Equal(after, before) {
		t.Errorf("labelled entries before %v, after %v", before, after)
	}
}

// TS-17-68: a real sandbox created with a 5-second TTL and never closed
// disappears shortly after its TTL. 17-PATH-4, 17-REQ-6.6, 17-REQ-6.7.
func TestSmokeTTL_TS17_68(t *testing.T) {
	engine, image := realEngine(t)
	ctx := context.Background()
	start := time.Now()
	s, err := newRealProvider(t, engine, Options{}).Create(ctx, sandbox.Spec{Image: image, TTL: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	id := s.ID()
	for engineLabelled(t, engine, id) != "" {
		if time.Since(start) > 35*time.Second {
			t.Fatalf("35s after a 5s TTL the engine still lists %s", engineLabelled(t, engine, id))
		}
		time.Sleep(time.Second)
	}
	if _, err := s.Tools(ctx, sandbox.ToolOptions{}); !errors.Is(err, sandbox.ErrUnavailable) && !errors.Is(err, sandbox.ErrClosed) {
		t.Errorf("Tools after the TTL = %v", err)
	}
}

// TS-17-69: Reap on a real engine removes an expired leftover volume and
// spares a future one. 17-PATH-5, 17-REQ-8.1, 17-REQ-8.2, 17-REQ-8.3.
func TestSmokeReap_TS17_69(t *testing.T) {
	engine, _ := realEngine(t)
	id1, id2 := hexID(8), hexID(8)
	for _, v := range []struct {
		id  string
		exp time.Time
	}{{id1, time.Now().Add(-time.Hour)}, {id2, time.Now().Add(time.Hour)}} {
		name := "agentkit-" + v.id
		t.Cleanup(func() { _ = exec.Command(engine, "volume", "rm", "-f", name).Run() })
		if b, err := exec.Command(engine, "volume", "create", "--label", "agentkit.sandbox="+v.id,
			"--label", "agentkit.sandbox.expires="+v.exp.UTC().Truncate(time.Second).Format(time.RFC3339), name).CombinedOutput(); err != nil {
			t.Fatalf("volume create %s: %v\n%s", name, err, b)
		}
	}
	ids, err := newRealProvider(t, engine, Options{}).Reap(context.Background())
	if err != nil || !slices.Contains(ids, id1) || slices.Contains(ids, id2) {
		t.Fatalf("Reap = %v, %v; want %s and not %s", ids, err, id1, id2)
	}
	vols := labelledAll(t, engine)
	if slices.Contains(vols, "agentkit-"+id1) || !slices.Contains(vols, "agentkit-"+id2) {
		t.Errorf("after Reap the engine lists %v", vols)
	}
}
