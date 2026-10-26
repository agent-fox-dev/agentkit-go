package local_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/localtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
)

func TestCapabilitiesClaimNoIsolationAndOnlyAnUnrestrictedNetwork(t *testing.T) {
	c := local.Unisolated().Capabilities()
	if c.Provider != "local" || c.Isolation != sandbox.IsolationNone || !slices.Equal(c.Network, []sandbox.NetworkMode{sandbox.NetworkUnrestricted}) {
		t.Errorf("capabilities %+v", c)
	}
	if c.Resources != (sandbox.ResourceCaps{}) || c.Image || c.Workdir || c.Landlock {
		t.Errorf("capabilities %+v claim a resource bound, image, workdir or Landlock", c)
	}
	if !strings.Contains(c.Notes, "process") || !strings.Contains(c.Notes, "privileges") {
		t.Errorf("Notes %q do not say the tools run in the embedder's process with its privileges", c.Notes)
	}
}

func TestCreateRefusesASpecAndLeavesNothingBehind(t *testing.T) {
	root := isolate(t)
	with := func(f func(*sandbox.Spec)) sandbox.Spec {
		s := sandbox.Spec{Network: unrestricted}
		f(&s)
		return s
	}
	for _, c := range []struct {
		field       string
		unsupported bool
		spec        sandbox.Spec
	}{
		{"Network.Mode", true, sandbox.Spec{}},
		{"Network.Mode", true, sandbox.Spec{Network: sandbox.Network{Mode: sandbox.NetworkAllowlist, Allow: []sandbox.Endpoint{{Host: "example.com", Port: 443}}}}},
		{"Resources.CPUs", true, with(func(s *sandbox.Spec) { s.Resources.CPUs = 1 })},
		{"Image", true, with(func(s *sandbox.Spec) { s.Image = "alpine" })},
		{"Workdir", true, with(func(s *sandbox.Spec) { s.Workdir = "/w" })},
		{"RequireLandlock", true, with(func(s *sandbox.Spec) { s.RequireLandlock = true })},
		{`option "a"`, false, with(func(s *sandbox.Spec) { s.Provider = map[string]any{"b": 1, "a": 2} })},
	} {
		sb, err := local.Unisolated().Create(context.Background(), c.spec)
		if sb != nil || err == nil || !strings.Contains(err.Error(), `"local"`) || !strings.Contains(err.Error(), c.field) {
			t.Errorf("%s: Create = %v, %v; want an error naming local and the field", c.field, sb, err)
		} else if errors.Is(err, sandbox.ErrUnsupported) != c.unsupported {
			t.Errorf("%s: %v matches ErrUnsupported %v, want %v", c.field, err, !c.unsupported, c.unsupported)
		}
	}
	if ents, _ := os.ReadDir(root); len(ents) != 0 {
		t.Errorf("%s holds %d entries", root, len(ents))
	}
	buf := make([]byte, 1<<22)
	if bytes.Contains(buf[:runtime.Stack(buf, true)], []byte("agent.(*Agent).Serve")) {
		t.Error("an agent is serving after the refused creates")
	}
}

func TestCreateFailsWhenItCannotMakeTheSandboxDirectory(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", notADir)
	if sb, err := local.Unisolated().Create(context.Background(), sandbox.Spec{Network: unrestricted}); sb != nil || err == nil {
		t.Errorf("Create under a TMPDIR that is a file = %v, %v; want an error", sb, err)
	}
	if ids, err := local.Unisolated().Reap(context.Background()); runtime.GOOS != "windows" && (ids != nil || err == nil) {
		t.Errorf("Reap under a TMPDIR that is a file = %v, %v; want an error", ids, err)
	}
}

// Create lays out a private directory with a marker and an in-process,
// unconfined agent; the sandbox outlives Create's context, and Close removes
// the directory.
func TestCreateLaysOutAPrivateSandboxThatCloseRemoves(t *testing.T) {
	isolate(t)
	before := time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	sb, err := local.Unisolated().Create(ctx, sandbox.Spec{Network: unrestricted, Labels: map[string]string{"team": "core"}})
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(sb.ID()) {
		t.Errorf("ID %q is not 16 hex characters", sb.ID())
	}
	d := dirOf(sb)
	for _, sub := range []string{"workspace", "home", "tmp"} {
		fi, err := os.Stat(filepath.Join(d, sub))
		if err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o700) {
			t.Errorf("%s: %v, %v; want a 0700 directory", sub, fi, err)
		}
	}
	var m struct {
		PID     int               `json:"pid"`
		Labels  map[string]string `json:"labels"`
		Created time.Time         `json:"created"`
		TTLNs   int64             `json:"ttl_ns"`
	}
	raw, err := os.ReadFile(filepath.Join(d, "agentkit-sandbox.json"))
	if err != nil || json.Unmarshal(raw, &m) != nil || m.PID != os.Getpid() || m.Labels["team"] != "core" ||
		time.Duration(m.TTLNs) != time.Hour || m.Created.Before(before.Add(-time.Second)) || m.Created.After(time.Now()) {
		t.Errorf("marker %s, %v", raw, err)
	}
	ws, _ := filepath.EvalSymlinks(filepath.Join(d, "workspace"))
	if sb.Agent().Workdir != ws || !reflect.DeepEqual(sb.Agent().Landlock, sandbox.LandlockInfo{Note: "in-process agent (local provider): no confinement"}) {
		t.Errorf("Agent() = %+v, want Workdir %s and no confinement", sb.Agent(), ws)
	}
	if r := proxy(t, sb, "list_files").Execute(context.Background(), []byte(`{"path":"."}`)); !r.OK {
		t.Errorf("list_files after Create's context was cancelled: %+v", r)
	}
	if _, err := sb.Sync(context.Background(), sandbox.GitRepo{URL: "https://example.test/r.git"}); !errors.Is(err, sandbox.ErrUnsupported) || !strings.Contains(err.Error(), "local") {
		t.Errorf("Sync(GitRepo) = %v, want ErrUnsupported naming local", err)
	}
	if err := sb.Close(context.Background()); err != nil || exists(d) {
		t.Errorf("Close = %v; the directory exists %v", err, exists(d))
	}
}

// Close removes the sandbox directory even when a build inside it left
// read-only entries under home/, the permissions Go gives extracted module
// cache entries (0444 files, 0555 directories) — the case in issue #182
// where a plain os.RemoveAll fails with "permission denied" because
// unlinking an entry needs write permission on its parent directory.
func TestCloseRemovesAHomeDirWithReadOnlyModuleCacheEntries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	isolate(t)
	sb := create(t, sandbox.Spec{Network: unrestricted})
	d := dirOf(sb)
	mod := filepath.Join(d, "home", "go", "pkg", "mod", "golang.org", "x", "sys@v0.48.0")
	if err := os.MkdirAll(mod, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mod, "go.mod"), []byte("module golang.org/x/sys\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	for p := mod; p != d; p = filepath.Dir(p) {
		if err := os.Chmod(p, 0o555); err != nil {
			t.Fatal(err)
		}
	}
	if err := sb.Close(context.Background()); err != nil || exists(d) {
		t.Errorf("Close = %v; the directory exists %v", err, exists(d))
	}
}

// The tools see Spec.Env, the host's PATH, LANG and TERM where Spec.Env lacks
// them, the sandbox's own HOME and TMPDIR, and the host's files.
func TestToolsRunWithTheSpecEnvPrivateDirsAndNoConfinement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX env and cat")
	}
	isolate(t)
	t.Setenv("LANG", "C.UTF-8")
	t.Setenv("TERM", "xterm")
	t.Setenv("HOST_SECRET", "1")
	host := testutil.Tree(t, map[string]string{"host.txt": "host content\n"})
	sb := create(t, sandbox.Spec{Network: unrestricted, Env: map[string]string{"FOO": "bar", "TERM": "dumb"}})
	d := dirOf(sb)
	out := proxy(t, sb, "execute").Execute(context.Background(), []byte(`{"command":"env; cat `+filepath.Join(host, "host.txt")+`"}`)).Text
	for _, want := range []string{"FOO=bar", "TERM=dumb", "LANG=C.UTF-8", "PATH=" + os.Getenv("PATH"),
		"HOME=" + filepath.Join(d, "home"), "TMPDIR=" + filepath.Join(d, "tmp"), "host content"} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("the output lacks %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, "HOST_SECRET") || strings.Contains(out, "TERM=xterm") {
		t.Errorf("the environment leaks the host's:\n%s", out)
	}
}

func TestTheTTLMakesTheSandboxUnavailableAndRemovesIt(t *testing.T) {
	isolate(t)
	sb := create(t, sandbox.Spec{Network: unrestricted, TTL: 300 * time.Millisecond})
	testutil.Eventually(t, "the sandbox directory to go at the TTL", func() bool { return !exists(dirOf(sb)) })
	if _, err := sb.Tools(context.Background(), sandbox.ToolOptions{}); !errors.Is(err, sandbox.ErrUnavailable) {
		t.Errorf("Tools after the TTL = %v, want ErrUnavailable", err)
	}
}

// Cutting the connection mid-call ends the call and makes every later call
// and operation fail as unavailable.
func TestACutConnectionMakesTheSandboxUnavailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell command")
	}
	isolate(t)
	sb := create(t, sandbox.Spec{Network: unrestricted})
	ex, rf := proxy(t, sb, "execute"), proxy(t, sb, "read_file")
	got := make(chan core.ToolResult, 1)
	go func() { got <- ex.Execute(context.Background(), []byte(`{"command":"echo $$ > p; sleep 60"}`)) }()
	testutil.Eventually(t, "the command to start", func() bool { return exists(filepath.Join(dirOf(sb), "workspace", "p")) })
	if err := localtest.Cut(sb); err != nil {
		t.Fatal(err)
	}
	if r := <-got; r.OK || r.Error != "sandbox_unavailable" || r.Detail == "" {
		t.Errorf("the in-flight call: %+v", r)
	}
	if r := rf.Execute(context.Background(), []byte(`{"path":"p"}`)); r.Error != "sandbox_unavailable" {
		t.Errorf("read_file after the cut: %+v", r)
	}
	if _, err := sb.Sync(context.Background(), sandbox.Tar{R: bytes.NewReader(make([]byte, 1024))}); !errors.Is(err, sandbox.ErrUnavailable) {
		t.Errorf("Sync after the cut = %v", err)
	}
	if err := sb.Close(context.Background()); err != nil || exists(dirOf(sb)) {
		t.Errorf("Close after the cut = %v; the directory exists %v", err, exists(dirOf(sb)))
	}
}
