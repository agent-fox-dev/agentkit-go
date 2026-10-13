//go:build linux || darwin

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TS-15-6: a root or system-ancestor --workdir or --landlock-rw, or a
// relative --landlock-ro or --landlock-rw, exits 2 with one stderr line
// naming the flag and serves nothing. 15-REQ-1.7.
func TestDangerousFlagValues_TS15_6(t *testing.T) {
	l := filepath.Join(t.TempDir(), "l")
	if err := os.Symlink("/usr", l); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ flag, value string }{
		{"--workdir", "/"}, {"--workdir", "/usr"}, {"--workdir", l}, {"--landlock-rw", "/"},
		{"--landlock-rw", "/opt"}, {"--landlock-rw", "rel/dir"}, {"--landlock-ro", "rel/dir"},
	} {
		so, se, code := runBinary(t, c.flag, c.value)
		if code != 2 || !oneLine(se) || !strings.Contains(se, c.flag) || so != "" {
			t.Errorf("%s %s: exit %d, stdout %q, stderr %q; want 2, nothing, one line naming the flag", c.flag, c.value, code, so, se)
		}
	}
}

// A relative --workdir is judged from the working directory: from /,
// --workdir . names / and is refused like --workdir /. 15-REQ-1.7.
func TestRelativeWorkdirFromRoot_TS15_6(t *testing.T) {
	cmd := exec.Command(binary, "--workdir", ".")
	cmd.Dir = "/"
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	}
	if code != 2 || !oneLine(se.String()) || !strings.Contains(se.String(), "--workdir") || so.Len() != 0 {
		t.Errorf("--workdir . from /: exit %d, stdout %q, stderr %q", code, so.String(), se.String())
	}
}

// TS-15-16: an unknown --network value exits 2 with one stderr line naming
// --network and serves nothing. 15-REQ-3.2.
func TestUnknownNetwork_TS15_16(t *testing.T) {
	for _, v := range []string{"bogus", "", "NONE"} {
		so, se, code := runBinary(t, "--network", v)
		if code != 2 || !oneLine(se) || !strings.Contains(se, "--network") || so != "" {
			t.Errorf("--network %q: exit %d, stdout %q, stderr %q", v, code, so, se)
		}
	}
}

// TS-15-17: --version prints protocol 1.2 and exits 0 without serving.
// 15-REQ-3.3.
func TestVersionProtocol_TS15_17(t *testing.T) {
	for _, args := range [][]string{{"--version"}, {"--version", "--network", "none", "--landlock-ro", "/usr"}} {
		so, se, code := runBinary(t, args...)
		if code != 0 || !strings.Contains(so, expectedVersion(t)) || !strings.Contains(so, "(protocol 1.2)") || se != "" {
			t.Errorf("%q: exit %d, stdout %q, stderr %q", args, code, so, se)
		}
	}
}

// TS-15-19 (off Linux): the command hands the stub's outcome to the agent:
// ABI 0 and the Linux-only note. 15-REQ-3.5. The Linux half is in
// landlock_linux_test.go.
func TestHandOffOffLinux_TS15_19(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("the Linux half is TestHandOff_TS15_19")
	}
	p := start(t, []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()}, "--workdir", t.TempDir())
	if l := p.sb.Agent().Landlock; l.ABI != 0 || l.Note != "Landlock is Linux-only (GOOS="+runtime.GOOS+")" {
		t.Errorf("Agent().Landlock = %+v", l)
	}
}
