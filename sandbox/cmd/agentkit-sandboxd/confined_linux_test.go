//go:build linux

package main

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// confinedDirs makes W, H, T and a sibling S holding S/secret.
func confinedDirs(t *testing.T) (w, h, tmp, s string) {
	t.Helper()
	base := t.TempDir()
	w, h, tmp, s = filepath.Join(base, "w"), filepath.Join(base, "h"), filepath.Join(base, "t"), filepath.Join(base, "s")
	for _, d := range []string{w, h, tmp, s} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(s, "secret"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	return w, h, tmp, s
}

// listing names every entry under dir with its size.
func listing(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil {
			out = append(out, fmt.Sprintf("%s %d", p, fi.Size()))
		}
		return nil
	})
	return out
}

// buildDialer builds the dialtcp helper into dir.
func buildDialer(t *testing.T, dir string) string {
	t.Helper()
	out := filepath.Join(dir, "dialtcp")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/dialtcp")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building dialtcp: %v\n%s", err, b)
	}
	return out
}

// listenLoopback opens a loopback listener (Multipath TCP off) that counts
// what it accepts.
func listenLoopback(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	var lc net.ListenConfig
	lc.SetMultipathTCP(false)
	l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	var n atomic.Int32
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			n.Add(1)
			_ = c.Close()
		}
	}()
	return l.Addr().String(), &n
}

func execIn(t *testing.T, sb sandbox.Sandbox, command string) (string, string) {
	t.Helper()
	r := tool(t, sb, "execute", fmt.Sprintf(`{"command":%q}`, command))
	return fmt.Sprint(r.Data["exit_code"]), r.Text
}

// TS-15-28: every agent RPC works against the confined agent and writes
// only beneath the workspace and the temporary directory. 15-REQ-5.1.
func TestConfinedRPCs_TS15_28(t *testing.T) {
	needABI(t, 2)
	w, h, tmp, s := confinedDirs(t)
	if err := os.WriteFile(filepath.Join(h, "profile"), []byte("p"), 0o644); err != nil {
		t.Fatal(err)
	}
	hBefore, sBefore := listing(t, h), listing(t, s)
	p := start(t, []string{"HOME=" + h, "TMPDIR=" + tmp, "PATH=/usr/bin:/bin"}, "--workdir", w)
	ctx := context.Background()
	sb := p.sb
	if _, err := sb.Tools(ctx, sandbox.ToolOptions{}); err != nil {
		t.Fatal(err)
	}
	if r := tool(t, sb, "write_file", `{"path":"notes/a.txt","content":"hello"}`); !r.OK {
		t.Errorf("write_file: %+v", r)
	}
	if r := tool(t, sb, "read_file", `{"path":"notes/a.txt"}`); !r.OK || !strings.Contains(fmt.Sprint(r.Data["content"]), "hello") {
		t.Errorf("read_file: %+v", r)
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range map[string]string{"tar/x.txt": "x", "tar/sub/y.txt": "y"} {
		_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: int64(len(body))})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	if _, err := sb.Sync(ctx, sandbox.Tar{R: &buf}); err != nil {
		t.Errorf("tar sync: %v", err)
	}
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, "pkg", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"go.mod": "module p\n", "pkg/deep/z.go": "package deep\n"} {
		if err := os.WriteFile(filepath.Join(project, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := sb.Sync(ctx, sandbox.LocalDir{Path: project, Ignore: workspace.NoGlobalExcludes()}); err != nil {
		t.Errorf("LocalDir sync: %v", err)
	}
	for name, want := range map[string]string{"tar/x.txt": "x", "tar/sub/y.txt": "y", "go.mod": "module p\n", "pkg/deep/z.go": "package deep\n"} {
		if b, err := os.ReadFile(filepath.Join(w, name)); err != nil || string(b) != want {
			t.Errorf("%s in W: %q, %v", name, b, err)
		}
	}
	// 200 KB is past the output limit, so the result spills.
	r := tool(t, sb, "execute", `{"command":"head -c 200000 /dev/zero | tr '\\0' x"}`)
	if r.Metadata == nil || !strings.HasPrefix(r.Metadata.SpillPath, filepath.Join(tmp, "agentkit-spill")+"/") {
		t.Errorf("the oversized execute spilled to %+v, want beneath T/agentkit-spill (agent stderr %q)", r.Metadata, p.stderr.String())
	}
	var out bytes.Buffer
	if err := sb.Export(ctx, "tar", sandbox.TarOut{W: &out}); err != nil {
		t.Errorf("tar export: %v", err)
	}
	var names []string
	for tr := tar.NewReader(&out); ; {
		h, err := tr.Next()
		if err != nil {
			break
		}
		names = append(names, h.Name)
	}
	for _, want := range []string{"x.txt", "sub/y.txt"} {
		if !slices.Contains(names, want) {
			t.Errorf("the exported tar %v lacks %s", names, want)
		}
	}
	dst := filepath.Join(t.TempDir(), "dst")
	if err := sb.Export(ctx, "pkg", sandbox.LocalDir{Path: dst, Ignore: workspace.NoGlobalExcludes()}); err != nil {
		t.Errorf("LocalDir export: %v", err)
	} else if _, err := os.Stat(filepath.Join(dst, "deep", "z.go")); err != nil {
		t.Errorf("dst lacks deep/z.go: %v", err)
	}
	if got := listing(t, h); !slices.Equal(got, hBefore) {
		t.Errorf("H changed: %v, was %v", got, hBefore)
	}
	if got := listing(t, s); !slices.Equal(got, sBefore) {
		t.Errorf("S changed: %v, was %v", got, sBefore)
	}
}

// TS-15-30: under unrestricted and allowlist the confined agent's tools
// reach a loopback listener while the filesystem stays confined.
// 15-REQ-5.5, 15-REQ-1.4, 15-REQ-2.2.
func TestConfinedNetworkOpen_TS15_30(t *testing.T) {
	needABI(t, 4)
	for _, mode := range []string{"unrestricted", "allowlist"} {
		w, h, tmp, s := confinedDirs(t)
		dial := buildDialer(t, w)
		addr, accepted := listenLoopback(t)
		p := start(t, []string{"HOME=" + h, "TMPDIR=" + tmp, "PATH=/usr/bin:/bin"}, "--network", mode, "--workdir", w)
		if code, out := execIn(t, p.sb, dial+" "+addr); code != "0" || accepted.Load() != 1 {
			t.Errorf("%s: dialtcp exit %s (%s), %d accepted", mode, code, out, accepted.Load())
		}
		l := p.sb.Agent().Landlock
		switch mode {
		case "unrestricted":
			if l.ABI != 4 || l.TCP || l.Note != "" {
				t.Errorf("unrestricted: Agent().Landlock = %+v", l)
			}
		case "allowlist":
			if l.ABI != 4 || l.TCP || !strings.Contains(l.Note, "allowlist") || !strings.Contains(l.Note, "provider") {
				t.Errorf("allowlist: Agent().Landlock = %+v", l)
			}
		}
		if code, out := execIn(t, p.sb, "touch "+filepath.Join(s, "x")); code == "0" || !strings.Contains(out, "Permission denied") {
			t.Errorf("%s: touch S/x exit %s: %s", mode, code, out)
		}
		if _, err := os.Lstat(filepath.Join(s, "x")); err == nil {
			t.Errorf("%s: S/x exists", mode)
		}
	}
}

// TS-15-31 (binary half): on a kernel offering exactly ABI 2 or 3 the
// filesystem ruleset applies at the kernel's ABI, TCP stays open and the
// note names the missing TCP support. 15-REQ-5.6. The simulated half is in
// confine's abi23_linux_test.go.
func TestConfinedOldKernel_TS15_31(t *testing.T) {
	k, err := kernelABI()
	if err != nil || k < 2 || k > 3 {
		t.Skipf("kernel Landlock ABI %d (%v); this half needs a kernel offering exactly ABI 2 or 3", k, err)
	}
	w, h, tmp, s := confinedDirs(t)
	dial := buildDialer(t, w)
	addr, _ := listenLoopback(t)
	p := start(t, []string{"HOME=" + h, "TMPDIR=" + tmp, "PATH=/usr/bin:/bin"}, "--network", "none", "--workdir", w)
	l := p.sb.Agent().Landlock
	if want := fmt.Sprintf("kernel Landlock ABI %d cannot restrict TCP (needs ABI 4, Linux 6.7)", k); l.ABI != k || l.TCP || l.Note != want {
		t.Errorf("Agent().Landlock = %+v", l)
	}
	if code, _ := execIn(t, p.sb, "touch "+filepath.Join(w, "a")); code != "0" {
		t.Error("touch W/a failed")
	}
	for _, c := range []string{"touch " + filepath.Join(s, "x"), "cat " + filepath.Join(s, "secret")} {
		if code, out := execIn(t, p.sb, c); code == "0" || !strings.Contains(out, "Permission denied") {
			t.Errorf("%s: exit %s: %s", c, code, out)
		}
	}
	if code, out := execIn(t, p.sb, dial+" "+addr); code != "0" {
		t.Errorf("loopback dial: exit %s: %s", code, out)
	}
}

// TS-15-32: under the ruleset, execute sees the agent's explicit environment
// unchanged and binaries under the system directories run. 15-REQ-5.7.
func TestConfinedEnvironment_TS15_32(t *testing.T) {
	needABI(t, 2)
	w, h, tmp, _ := confinedDirs(t)
	goPath, goErr := exec.LookPath("go")
	pathVar := "/usr/bin:/bin"
	if goErr == nil {
		pathVar += ":" + filepath.Dir(goPath)
	}
	t.Setenv("TEST_ONLY_VAR", "1")
	env := []string{"PATH=" + pathVar, "HOME=" + h, "TMPDIR=" + tmp, "LANG=C.UTF-8", "MARKER=landlock-env"}
	p := start(t, env, "--workdir", w)
	_, out := execIn(t, p.sb, "env")
	var got []string
	for _, line := range strings.Split(out, "\n") {
		k, _, ok := strings.Cut(line, "=")
		if !ok || slices.Contains([]string{"PWD", "OLDPWD", "SHLVL", "_"}, k) {
			continue
		}
		got = append(got, line)
	}
	want := slices.Clone(env)
	sort.Strings(got)
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Errorf("env under execute:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if code, out := execIn(t, p.sb, "/bin/sh -c true"); code != "0" {
		t.Errorf("/bin/sh -c true: exit %s: %s", code, out)
	}
	if goErr != nil {
		t.Skipf("go is not on PATH: %v", goErr)
	}
	real, _ := filepath.EvalSymlinks(goPath)
	beneath := false
	for _, d := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/libx32", "/etc", "/opt"} {
		if strings.HasPrefix(real, d+"/") {
			beneath = true
		}
	}
	if !beneath {
		t.Skipf("go resolves to %s, not beneath a system directory", real)
	}
	if code, out := execIn(t, p.sb, "go version"); code != "0" {
		t.Errorf("go version: exit %s: %s", code, out)
	}
}
