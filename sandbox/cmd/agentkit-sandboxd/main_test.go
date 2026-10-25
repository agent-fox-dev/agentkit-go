package main

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/buildver"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

func TestParseFlagsReadsTheCommandLine(t *testing.T) {
	spec := func(ttl time.Duration, mode sandbox.NetworkMode, wd string) []string {
		return sandbox.AgentArgs(sandbox.Spec{TTL: ttl, Network: sandbox.Network{Mode: mode}, Workdir: wd})
	}
	for _, c := range []struct {
		args []string
		want config
	}{
		{nil, config{workdir: "/workspace", ttl: time.Hour, network: sandbox.NetworkNone}},
		{spec(90*time.Second, sandbox.NetworkUnrestricted, "/w"), config{workdir: "/w", ttl: 90 * time.Second, network: sandbox.NetworkUnrestricted}},
		{spec(48*time.Hour, sandbox.NetworkAllowlist, ""), config{workdir: "/workspace", ttl: 48 * time.Hour, network: sandbox.NetworkAllowlist}},
		{[]string{"--landlock-ro", "/a", "--landlock-rw", "/x", "--landlock-ro", "/b", "--network", "none"},
			config{workdir: "/workspace", ttl: time.Hour, landlockRO: []string{"/a", "/b"}, landlockRW: []string{"/x"}}},
	} {
		got, err := parseFlags(c.args)
		if got.networkFlag = ""; err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseFlags(%q) = %+v, %v; want %+v", c.args, got, err, c.want)
		}
	}
	for args, want := range map[string]string{
		"--ttl -1s": "--ttl must be positive, got -1s", "--ttl bogus": `invalid value "bogus" for flag -ttl`,
		"--nosuchflag": "flag provided but not defined: -nosuchflag", "/w": `unexpected argument "/w"`,
		"--network NONE": `--network: invalid value "NONE" (want none, allowlist or unrestricted)`,
	} {
		if _, err := parseFlags(strings.Fields(args)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("parseFlags(%s) = %v, want %q", args, err, want)
		}
	}
}

// A bad flag, or a path flag widening the boundary, exits 2 with one stderr
// line naming it; a workspace the ruleset or the agent cannot use exits 1.
func TestRunRefusesWhatItCannotServe(t *testing.T) {
	usr, missing := filepath.Join(t.TempDir(), "usr"), filepath.Join(t.TempDir(), "missing")
	if err := os.Symlink("/usr", usr); err != nil {
		t.Fatal(err)
	}
	t.Chdir("/")
	for _, c := range []struct {
		flag, value string
		code        int
	}{{"--ttl", "0", 2}, {"--network", "", 2}, {"--workdir", "/", 2}, {"--workdir", ".", 2}, {"--workdir", usr, 2},
		{"--landlock-rw", "/opt", 2}, {"--landlock-rw", "rel/dir", 2}, {"--landlock-ro", "rel/dir", 2}, {"--workdir", missing, 1}} {
		want := map[int]string{2: c.flag, 1: missing}[c.code]
		if so, se, code := runIn(t, c.flag, c.value); code != c.code || so != "" || !oneLine(se) || !strings.Contains(se, want) {
			t.Errorf("%s %s: exit %d, stdout %q, stderr %q; want %d and one line naming %s", c.flag, c.value, code, so, se, c.code, want)
		}
	}
}

// --version prints the versions, and any commit and build time stamped, first.
func TestVersionPrintsTheVersions(t *testing.T) {
	proto := fmt.Sprintf("(protocol %d.%d)", sandbox.ProtocolMajor, sandbox.ProtocolMinor)
	if so, se, code := runIn(t, "--version"); code != 0 || se != "" || so != "agentkit-sandboxd "+buildver.Version()+" "+proto+"\n" {
		t.Errorf("--version in process: exit %d, stdout %q, stderr %q", code, so, se)
	}
	out, err := exec.Command(binary, "--version", "--workdir", "/").CombinedOutput()
	if want := "agentkit-sandboxd v9.8.7 " + proto + " commit abc1234 built 2026-10-14T00:00:00Z\n"; err != nil || string(out) != want {
		t.Errorf("--version: %v, %q; want %q", err, out, want)
	}
}

func TestRedirectStdoutSendsStrayWritesToStderr(t *testing.T) {
	origOut, origErr := os.Stdout, os.Stderr
	f, _ := os.Create(filepath.Join(t.TempDir(), "stderr"))
	os.Stderr = f
	got := redirectStdout()
	fmt.Println("stray")
	os.Stdout, os.Stderr = origOut, origErr
	if b, _ := os.ReadFile(f.Name()); got != origOut || string(b) != "stray\n" {
		t.Errorf("redirectStdout returned %v (stdout %v); stderr got %q", got, origOut, b)
	}
}

// The agent serves its stdio (no socket) until the host closes, running the
// tools in --workdir with its environment verbatim, through constant GC; on
// Linux it and its tools are confined to W, HOME and TMPDIR, as Hello says.
func TestServesAConfinedAgentOverItsStdio(t *testing.T) {
	base := testutil.Tree(t, map[string]string{"W/seed.txt": "seed\n", "H/.keep": "", "T/.keep": "", "S/secret": "s"})
	d := map[string]string{}
	for _, n := range []string{"W", "H", "T", "S"} {
		d[n], _ = filepath.EvalSymlinks(filepath.Join(base, n))
	}
	// dialtcp to 127.0.0.1:1 is refused with TCP open, denied under Landlock.
	dial := filepath.Join(d["W"], "dialtcp")
	build := exec.Command("go", "build", "-o", dial, "./testdata/dialtcp")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building dialtcp: %v\n%s", err, b)
	}
	dial += " 127.0.0.1:1"
	t.Setenv("HOST_ONLY_VAR", "1")
	p := start(t, []string{"PATH=/usr/bin:/bin", "MARKER=sandboxd-env", "GOGC=1", "HOME=" + d["H"], "TMPDIR=" + d["T"]}, "--workdir", d["W"])
	a, l := p.sb.Agent(), p.sb.Agent().Landlock
	if a.ProtocolMajor != sandbox.ProtocolMajor || a.Workdir != d["W"] {
		t.Errorf("Agent() = %+v", a)
	}
	if r := p.tool(t, "list_files", `{"path":"."}`); !r.OK || !strings.Contains(r.Text, "seed.txt") {
		t.Errorf("list_files: %+v", r)
	}
	if _, out := p.sh(t, "env"); !strings.Contains(out, "MARKER=sandboxd-env") || strings.Contains(out, "HOST_ONLY_VAR") {
		t.Errorf("execute env:\n%s", out)
	}
	for i := range 30 {
		if code, out := p.sh(t, "echo hi"); code != "0" || out != "hi\n" {
			t.Fatalf("call %d: exit %s, %q (stderr %q)", i, code, out, p.stderr)
		}
	}
	fds, _ := filepath.Glob(fmt.Sprintf("/proc/%d/fd/*", p.cmd.Process.Pid))
	for _, fd := range fds {
		if link, _ := os.Readlink(fd); strings.HasPrefix(link, "socket:") {
			t.Errorf("%s is a socket", fd)
		}
	}
	ctx := context.Background()
	project := testutil.Tree(t, map[string]string{"go.mod": "module p\n", "pkg/deep/z.go": "package deep\n"})
	if _, err := p.sb.Sync(ctx, sandbox.LocalDir{Path: project, Ignore: workspace.NoGlobalExcludes()}); err != nil {
		t.Errorf("LocalDir sync: %v", err)
	}
	var out bytes.Buffer
	if err := p.sb.Export(ctx, "pkg", sandbox.TarOut{W: &out}); err != nil {
		t.Errorf("tar export: %v", err)
	} else if h, err := tar.NewReader(&out).Next(); err != nil || !strings.HasPrefix(h.Name, "deep") {
		t.Errorf("the exported tar starts with %+v, %v", h, err)
	}
	// 200 KB is past the output limit, so the result spills into T.
	r := p.tool(t, "execute", `{"command":"head -c 200000 /dev/zero | tr '\\0' x"}`)
	if r.Metadata == nil || !strings.HasPrefix(r.Metadata.SpillPath, d["T"]+"/agentkit-spill/") {
		t.Errorf("the oversized execute spilled to %+v (stderr %q)", r.Metadata, p.stderr)
	}
	if runtime.GOOS != "linux" && l.Note != "Landlock is Linux-only (GOOS="+runtime.GOOS+")" {
		t.Errorf("Agent().Landlock = %+v", l)
	}
	if k := l.KernelABI; l.ABI >= 2 {
		if l.ABI != min(k, 4) || l.TCP != (k >= 4) || !slices.Equal(l.Writable[:3], []string{d["W"], d["H"], d["T"]}) {
			t.Errorf("Agent().Landlock = %+v, want ABI %d and W, H, T writable", l, min(k, 4))
		}
		for _, c := range []string{"touch " + d["S"] + "/x", "cat " + d["S"] + "/secret", `/bin/sh -c "/bin/sh -c 'echo x > ` + d["S"] + `/y'"`} {
			if code, out := p.sh(t, c); code == "0" || !strings.Contains(out, "Permission denied") {
				t.Errorf("%s: exit %s: %s", c, code, out)
			}
		}
		if _, out := p.sh(t, dial); !strings.Contains(out, map[bool]string{true: "permission denied", false: "connection refused"}[k >= 4]) {
			t.Errorf("dialtcp under none at ABI %d: %s", k, out)
		}
	}
	if err := errors.Join(p.sb.Close(ctx), p.cmd.Wait()); err != nil {
		t.Errorf("closing: %v (stderr %q)", err, p.stderr)
	}
	if l.ABI < 2 {
		return
	}
	missing := filepath.Join(d["S"], "missing")
	p = start(t, []string{"HOME=/", "TMPDIR=" + missing, "PATH=/usr/bin:/bin"}, "--network", "unrestricted", "--workdir", d["W"])
	if l := p.sb.Agent().Landlock; l.ABI < 2 || l.TCP || slices.Contains(l.Writable, "/") ||
		!strings.Contains(l.Note, `HOME="/"`) || !strings.Contains(l.Note, `TMPDIR="`+missing) {
		t.Errorf("unusable HOME and TMPDIR: Agent().Landlock = %+v", l)
	}
	if _, out := p.sh(t, dial); !strings.Contains(out, "connection refused") {
		t.Errorf("dialtcp under unrestricted: %s", out)
	}
	if code, _ := p.sh(t, "touch "+d["S"]+"/x"); code == "0" {
		t.Error("touch S/x succeeded under unrestricted")
	}
}

func TestExitsOneWhenTheTTLExpires(t *testing.T) {
	p := start(t, []string{"HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()}, "--workdir", t.TempDir(), "--ttl", "300ms")
	var ee *exec.ExitError
	if err := p.cmd.Wait(); !errors.As(err, &ee) || ee.ExitCode() != 1 || !oneLine(p.stderr.String()) || !strings.Contains(p.stderr.String(), "300ms") {
		t.Errorf("after the TTL: %v, stderr %q", err, p.stderr)
	}
}
