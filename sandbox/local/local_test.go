package local_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/attach"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/fakeagent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/localtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/stdioconn"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
)

var unrestricted = sandbox.Network{Mode: sandbox.NetworkUnrestricted}

// isolate points TMPDIR at a fresh directory and returns local's root under
// it.
func isolate(t *testing.T) string {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())
	return filepath.Join(os.TempDir(), "agentkit-sandbox-local")
}

// create makes a local sandbox closed at the end of the test.
func create(t *testing.T, spec sandbox.Spec) sandbox.Sandbox {
	t.Helper()
	sb, err := local.Unisolated().Create(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sb.Close(context.Background()) })
	return sb
}

// sandboxDir is the directory of a local sandbox.
func sandboxDir(sb sandbox.Sandbox) string {
	return filepath.Join(os.TempDir(), "agentkit-sandbox-local", sb.ID())
}

// proxy returns the named proxy tool of sb.
func proxy(t *testing.T, sb sandbox.Sandbox, name string) core.Tool {
	t.Helper()
	tl, err := sb.Tools(context.Background(), sandbox.ToolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range tl {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("no proxy %s", name)
	return core.Tool{}
}

func entries(dir string) []os.DirEntry {
	ents, _ := os.ReadDir(dir)
	return ents
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// agentServing reports whether any goroutine is running an agent service.
func agentServing() bool {
	buf := make([]byte, 1<<22)
	return bytes.Contains(buf[:runtime.Stack(buf, true)], []byte("agent.(*Agent).Serve"))
}

// TS-13-12: a refused spec, including an unknown Spec.Provider key, leaves
// no sandbox resources behind. 13-REQ-2.5, 13-REQ-2.6.
func TestRefusedSpecLeavesNothing_TS13_12(t *testing.T) {
	root := isolate(t)
	ctx := context.Background()
	sb, err := local.Unisolated().Create(ctx, sandbox.Spec{Network: unrestricted, Provider: map[string]any{"bogus_key": 1}})
	if sb != nil || err == nil || !strings.Contains(err.Error(), "bogus_key") {
		t.Fatalf("Create with bogus_key = %v, %v", sb, err)
	}
	sb, err = local.Unisolated().Create(ctx, sandbox.Spec{})
	if sb != nil || !errors.Is(err, sandbox.ErrUnsupported) {
		t.Fatalf("Create with the zero Spec = %v, %v", sb, err)
	}
	if n := len(entries(root)); n != 0 {
		t.Errorf("%s holds %d entries", root, n)
	}
	if agentServing() {
		t.Error("an agent service is running after refused creates")
	}
}

// TS-13-32: cancelling the Create context after Create returns does not end
// the sandbox. 13-REQ-5.5.
func TestCreateContextBoundsCreationOnly_TS13_32(t *testing.T) {
	isolate(t)
	cctx, cancel := context.WithCancel(context.Background())
	sb, err := local.Unisolated().Create(cctx, sandbox.Spec{Network: unrestricted})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sb.Close(context.Background()) })
	cancel()
	time.Sleep(200 * time.Millisecond)
	tl, err := sb.Tools(context.Background(), sandbox.ToolOptions{})
	if err != nil || len(tl) != 11 {
		t.Fatalf("Tools after the cancel: %d, %v", len(tl), err)
	}
	if r := proxy(t, sb, "list_files").Execute(context.Background(), []byte(`{"path":"."}`)); !r.OK {
		t.Fatalf("list_files after the cancel: %+v", r)
	}
}

// TS-13-33: at the TTL the host timer makes the sandbox unavailable and
// destroys it, and an agent that ends makes it unavailable. 13-REQ-5.6.
func TestTTLAndAgentEnd_TS13_33(t *testing.T) {
	isolate(t)
	sb := create(t, sandbox.Spec{Network: unrestricted, TTL: 500 * time.Millisecond})
	dir := sandboxDir(sb)
	time.Sleep(1500 * time.Millisecond)
	if _, err := sb.Tools(context.Background(), sandbox.ToolOptions{}); !errors.Is(err, sandbox.ErrUnavailable) {
		t.Errorf("Tools after the TTL = %v, want ErrUnavailable", err)
	}
	if exists(dir) {
		t.Errorf("%s still exists after the TTL", dir)
	}

	f := fakeagent.New()
	fsb, err := attach.Dial(context.Background(), f.Serve(), attach.Config{Spec: sandbox.Spec{TTL: time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	defer fsb.Close(context.Background())
	f.Stop()
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, err := fsb.Sync(context.Background(), sandbox.Tar{R: bytes.NewReader(make([]byte, 1024))})
		if errors.Is(err, sandbox.ErrUnavailable) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Sync after the agent ended = %v, want ErrUnavailable within 2s", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TS-13-56: local reports no isolation, only unrestricted network, no
// resources, image or workdir, and a plain note. 13-REQ-9.1.
func TestCapabilities_TS13_56(t *testing.T) {
	c := local.Unisolated().Capabilities()
	if c.Provider != "local" || c.Isolation != sandbox.IsolationNone || !slices.Equal(c.Network, []sandbox.NetworkMode{sandbox.NetworkUnrestricted}) {
		t.Errorf("capabilities %+v", c)
	}
	if c.Resources != (sandbox.ResourceCaps{}) || c.Image || c.Workdir {
		t.Errorf("capabilities %+v claim a resource, image or workdir", c)
	}
	if !strings.Contains(c.Notes, "process") || !strings.Contains(c.Notes, "privileges") {
		t.Errorf("Notes %q", c.Notes)
	}
}

// TS-13-57: local refuses the zero network, allowlist, resources, image,
// workdir and provider options without creating a directory. 13-REQ-9.2.
func TestRefusals_TS13_57(t *testing.T) {
	root := isolate(t)
	with := func(f func(*sandbox.Spec)) sandbox.Spec {
		s := sandbox.Spec{Network: unrestricted}
		f(&s)
		return s
	}
	cases := []struct {
		field      string
		capability bool
		spec       sandbox.Spec
	}{
		{"Network.Mode", true, sandbox.Spec{}},
		{"Network.Mode", true, sandbox.Spec{Network: sandbox.Network{Mode: sandbox.NetworkAllowlist, Allow: []sandbox.Endpoint{{Host: "example.com", Port: 443}}}}},
		{"Resources.CPUs", true, with(func(s *sandbox.Spec) { s.Resources.CPUs = 1 })},
		{"Resources.MemoryBytes", true, with(func(s *sandbox.Spec) { s.Resources.MemoryBytes = 1 })},
		{"Resources.PIDs", true, with(func(s *sandbox.Spec) { s.Resources.PIDs = 1 })},
		{"Resources.DiskBytes", true, with(func(s *sandbox.Spec) { s.Resources.DiskBytes = 1 })},
		{"Image", true, with(func(s *sandbox.Spec) { s.Image = "alpine" })},
		{"Workdir", true, with(func(s *sandbox.Spec) { s.Workdir = "/w" })},
		{"x", false, with(func(s *sandbox.Spec) { s.Provider = map[string]any{"x": 1} })},
	}
	for _, tc := range cases {
		sb, err := local.Unisolated().Create(context.Background(), tc.spec)
		if sb != nil || err == nil || !strings.Contains(err.Error(), `"local"`) || !strings.Contains(err.Error(), tc.field) {
			t.Errorf("%s: Create = %v, %v", tc.field, sb, err)
			continue
		}
		if errors.Is(err, sandbox.ErrUnsupported) != tc.capability {
			t.Errorf("%s: ErrUnsupported = %v, want %v", tc.field, errors.Is(err, sandbox.ErrUnsupported), tc.capability)
		}
	}
	if n := len(entries(root)); n != 0 {
		t.Errorf("%s holds %d entries", root, n)
	}
}

// TS-13-58: Create lays out a private 0700 directory tree with a marker,
// and Close removes it. 13-REQ-9.3, 13-REQ-9.6.
func TestLayoutAndClose_TS13_58(t *testing.T) {
	isolate(t)
	before := time.Now()
	sb, err := local.Unisolated().Create(context.Background(), sandbox.Spec{Network: unrestricted, Labels: map[string]string{"team": "core"}})
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(sb.ID()) {
		t.Errorf("ID %q", sb.ID())
	}
	d := sandboxDir(sb)
	for _, sub := range []string{"workspace", "home", "tmp"} {
		fi, err := os.Stat(filepath.Join(d, sub))
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o700 {
			t.Errorf("%s has mode %v, want 0700", sub, fi.Mode().Perm())
		}
	}
	b, err := os.ReadFile(filepath.Join(d, "agentkit-sandbox.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		PID     int               `json:"pid"`
		Labels  map[string]string `json:"labels"`
		Created time.Time         `json:"created"`
		TTLNs   int64             `json:"ttl_ns"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m.PID != os.Getpid() || m.Labels["team"] != "core" || m.Created.Before(before.Add(-time.Second)) || m.Created.After(time.Now().Add(time.Second)) || time.Duration(m.TTLNs) != time.Hour {
		t.Errorf("marker %s", b)
	}
	ws, _ := filepath.EvalSymlinks(filepath.Join(d, "workspace"))
	if sb.Agent().Workdir != ws {
		t.Errorf("Agent().Workdir = %q, want %q", sb.Agent().Workdir, ws)
	}
	if err := sb.Close(context.Background()); err != nil || exists(d) {
		t.Errorf("Close = %v; directory exists %v", err, exists(d))
	}
}

// TS-13-59: the local agent's environment is Spec.Env plus host PATH, LANG
// and TERM where absent, plus private HOME and TMPDIR. 13-REQ-9.4.
func TestEnvironment_TS13_59(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX env command")
	}
	isolate(t)
	t.Setenv("LANG", "C.UTF-8")
	t.Setenv("TERM", "xterm")
	t.Setenv("HOST_SECRET", "1")
	sb := create(t, sandbox.Spec{Network: unrestricted, Env: map[string]string{"FOO": "bar", "TERM": "dumb"}})
	d := sandboxDir(sb)
	out := proxy(t, sb, "execute").Execute(context.Background(), []byte(`{"command":"env"}`)).Text
	for _, want := range []string{"FOO=bar", "TERM=dumb", "LANG=C.UTF-8", "PATH=" + os.Getenv("PATH"),
		"HOME=" + filepath.Join(d, "home"), "TMPDIR=" + filepath.Join(d, "tmp")} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("env lacks %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, "HOST_SECRET") || strings.Contains(out, "TERM=xterm") {
		t.Errorf("env leaks the host:\n%s", out)
	}
}

// TS-13-60: local runs the agent over os.Pipe through the command's own
// net.Conn adapter and applies no confinement. 13-REQ-9.5.
func TestSharedAdapterNoConfinement_TS13_60(t *testing.T) {
	r1, w1, _ := os.Pipe()
	r2, w2, _ := os.Pipe()
	a, b := stdioconn.New(r1, w2), stdioconn.New(r2, w1)
	if _, err := a.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2)
	if _, err := io.ReadFull(b, buf); err != nil || string(buf) != "hi" || a.LocalAddr() == nil {
		t.Fatalf("adapter round trip: %q, %v", buf, err)
	}
	a.Close()
	if _, err := b.Read(buf); err != io.EOF {
		t.Errorf("peer read after Close = %v", err)
	}
	b.Close()

	const adapter = "github.com/agent-fox-dev/agentkit-go/sandbox/internal/stdioconn"
	for _, pkg := range []string{"github.com/agent-fox-dev/agentkit-go/sandbox/local", "github.com/agent-fox-dev/agentkit-go/sandbox/cmd/agentkit-sandboxd"} {
		out, err := exec.Command("go", "list", "-deps", pkg).Output()
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(strings.Fields(string(out)), adapter) {
			t.Errorf("%s does not depend on %s", pkg, adapter)
		}
	}

	if runtime.GOOS == "windows" {
		return
	}
	isolate(t)
	host := filepath.Join(t.TempDir(), "host.txt")
	if err := os.WriteFile(host, []byte("host content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sb := create(t, sandbox.Spec{Network: unrestricted})
	if r := proxy(t, sb, "execute").Execute(context.Background(), []byte(`{"command":"cat `+host+`"}`)); !r.OK || !strings.Contains(r.Text, "host content") {
		t.Errorf("cat of a host file: %+v", r)
	}
}

// TS-13-72 (smoke, 13-PATH-4): cutting the connection mid-call makes the
// sandbox sticky-unavailable for every later proxy call and operation.
// 13-REQ-7.5, 13-REQ-9.8, 13-REQ-5.8.
func TestSmokeConnectionCut_TS13_72(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell command")
	}
	isolate(t)
	sb, err := local.Unisolated().Create(context.Background(), sandbox.Spec{Network: unrestricted})
	if err != nil {
		t.Fatal(err)
	}
	dir := sandboxDir(sb)
	ex, rf := proxy(t, sb, "execute"), proxy(t, sb, "read_file")
	got := make(chan core.ToolResult, 1)
	go func() { got <- ex.Execute(context.Background(), []byte(`{"command":"echo $$ > p; sleep 60"}`)) }()
	pfile := filepath.Join(dir, "workspace", "p")
	for deadline := time.Now().Add(5 * time.Second); !exists(pfile); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the command did not start")
		}
	}
	if err := localtest.Cut(sb); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-got:
		if r.OK || r.Error != "sandbox_unavailable" || r.Detail == "" {
			t.Errorf("the in-flight call: %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the in-flight call did not end after the cut")
	}
	if r := rf.Execute(context.Background(), []byte(`{"path":"p"}`)); r.Error != "sandbox_unavailable" {
		t.Errorf("read_file after the cut: %+v", r)
	}
	if _, err := sb.Sync(context.Background(), sandbox.Tar{R: bytes.NewReader(make([]byte, 1024))}); !errors.Is(err, sandbox.ErrUnavailable) {
		t.Errorf("Sync after the cut = %v", err)
	}
	if _, err := sb.Tools(context.Background(), sandbox.ToolOptions{}); !errors.Is(err, sandbox.ErrUnavailable) {
		t.Errorf("Tools after the cut = %v", err)
	}
	_ = sb.Close(context.Background())
	if exists(dir) {
		t.Errorf("%s survived Close", dir)
	}
}
