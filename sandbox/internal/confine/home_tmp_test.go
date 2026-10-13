package confine

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TS-15-5 (report cases): an unset, relative, missing, root or
// system-directory HOME or temporary directory is skipped with a note
// naming the variable and the value, while the rest still applies.
// 15-REQ-1.6. The binary case is in agentkit-sandboxd's
// landlock_linux_test.go.
func TestHomeTmpSkipped_TS15_5(t *testing.T) {
	base := t.TempDir()
	w, home, tmp := filepath.Join(base, "w"), filepath.Join(base, "h"), filepath.Join(base, "t")
	for _, d := range []string{w, home, tmp} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	l := filepath.Join(base, "l")
	if err := os.Symlink("/usr", l); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	x := filepath.Join(base, "missing")
	for _, c := range []struct {
		env            []string
		variable, show string // the variable the note names, and the value it quotes
		skipped        string // the value that must not be writable
		other          string // the other root, still writable
	}{
		{[]string{"TMPDIR=" + tmp}, "HOME", "unset", "", tmp},
		{[]string{"HOME=rel/home", "TMPDIR=" + tmp}, "HOME", "rel/home", "rel/home", tmp},
		{[]string{"HOME=" + x, "TMPDIR=" + tmp}, "HOME", x, x, tmp},
		{[]string{"HOME=/", "TMPDIR=" + tmp}, "HOME", `"/"`, "/", tmp},
		{[]string{"HOME=/etc", "TMPDIR=" + tmp}, "HOME", "/etc", "/etc", tmp},
		{[]string{"HOME=" + l, "TMPDIR=" + tmp}, "HOME", l, l, tmp},
		{[]string{"HOME=" + home, "TMPDIR=rel/tmp"}, "TMPDIR", "rel/tmp", "rel/tmp", home},
		{[]string{"HOME=" + home, "TMPDIR=" + x}, "TMPDIR", x, x, home},
		{[]string{"HOME=" + home, "TMPDIR=/"}, "TMPDIR", `"/"`, "/", home},
		{[]string{"HOME=" + home, "TMPDIR=/usr"}, "TMPDIR", "/usr", "/usr", home},
		{[]string{"HOME=" + home, "TEMP=" + x}, "TEMP", x, x, home},
	} {
		p, err := buildRules(Inputs{Workdir: w, Env: c.env}, 3)
		if err != nil {
			t.Errorf("%v: %v", c.env, err)
			continue
		}
		note := strings.Join(p.notes, "; ")
		if !strings.Contains(note, c.variable) || !strings.Contains(note, c.show) {
			t.Errorf("%v: notes %q lack %s and %s", c.env, note, c.variable, c.show)
		}
		if c.skipped != "" {
			for _, cand := range []string{c.skipped, resolvedOr(c.skipped)} {
				if slices.Contains(p.writable, cand) {
					t.Errorf("%v: %s is writable", c.env, cand)
				}
			}
		}
		for _, keep := range []string{w, c.other} {
			if !slices.Contains(p.writable, resolvedOr(keep)) {
				t.Errorf("%v: %s is not writable: %v", c.env, keep, p.writable)
			}
		}
	}
}
