package confine

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The writable roots get every right of the ABI, the system and
// --landlock-ro roots read and execute, /proc read only and the device files
// read and write (and truncate from ABI 3), in that order.
func TestBuildRulesGrantsEachRootItsRightsInOrder(t *testing.T) {
	d := dirs(t, "W", "H", "T", "R", "O")
	in := Inputs{Workdir: d["W"], Env: []string{"HOME=" + d["H"], "TMPDIR=" + d["T"]}, RW: []string{d["R"]}, RO: []string{d["O"]}}
	abi1 := accExecute | accWriteFile | accReadFile | accReadDir | accRemoveDir | accRemoveFile | accMakeChar |
		accMakeDir | accMakeReg | accMakeSock | accMakeFifo | accMakeBlock | accMakeSym
	roots, rx := resolved(d["W"], d["H"], d["T"], d["R"]), resolved(append(slices.Clone(systemDirs), d["O"])...)
	proc, devices := resolved("/proc"), resolved(deviceFiles...)
	for _, c := range []struct {
		abi       int
		full, dev access
	}{
		{2, abi1 | accRefer, accReadFile | accWriteFile},
		{3, abi1 | accRefer | accTruncate, accReadFile | accWriteFile | accTruncate},
		{4, abi1 | accRefer | accTruncate, accReadFile | accWriteFile | accTruncate},
	} {
		var want []rule
		for _, g := range []struct {
			paths []string
			acc   access
		}{{roots, c.full}, {rx, accReadFile | accReadDir | accExecute}, {proc, accReadFile | accReadDir}, {devices, c.dev}} {
			for _, path := range g.paths {
				want = append(want, rule{path, g.acc})
			}
		}
		p, err := buildRules(in, c.abi)
		if w, r := slices.Concat(roots, devices), slices.Concat(rx, proc); err != nil || !slices.Equal(p.rules, want) ||
			!slices.Equal(p.writable, w) || !slices.Equal(p.readOnly, r) {
			t.Errorf("ABI %d: %v, rules %v, writable %v, read-only %v; want %v, %v, %v", c.abi, err, p.rules, p.writable, p.readOnly, want, w, r)
		}
	}
}

// Every root is listed resolved through symlinks (a relative workspace
// against the working directory), once: a root resolving to one already
// added collapses into it. Missing system and device paths are skipped
// silently, missing flag paths with a note.
func TestBuildRulesListsEachExistingRootOnceResolved(t *testing.T) {
	extendLists(t, []string{"/nonexistent-agentkit-sys"}, []string{"/dev/nonexistent-agentkit"})
	d := dirs(t, "w", "a", "b", "c", "x")
	link := func(name, target string) string {
		if err := os.Symlink(target, filepath.Join(d["base"], name)); err != nil {
			t.Fatal(err)
		}
		return filepath.Join(d["base"], name)
	}
	link("lw", link("lw0", d["w"]))
	m1, m2 := filepath.Join(d["base"], "m1"), filepath.Join(d["base"], "m2")
	t.Chdir(d["base"])
	in := Inputs{Workdir: "lw", Env: []string{"HOME=" + d["w"] + "/", "TMPDIR=" + filepath.Join(d["x"], "..", "a")},
		RW: []string{link("la", d["a"]), m2, d["b"]}, RO: []string{d["b"], link("lc", d["c"]), m1, d["c"]}}
	p, err := buildRules(in, 4)
	if err != nil {
		t.Fatal(err)
	}
	wantW := slices.Concat(resolved(d["w"], d["a"], d["b"]), resolved(deviceFiles...))
	wantR := slices.Concat(resolved(systemDirs...), resolved(d["c"], "/proc"))
	notes := []string{"--landlock-rw " + m2 + " does not exist; skipped", "--landlock-ro " + m1 + " does not exist; skipped"}
	if !slices.Equal(p.writable, wantW) || !slices.Equal(p.readOnly, wantR) || len(p.rules) != len(wantW)+len(wantR) ||
		p.rules[0].path != wantW[0] || !slices.Equal(p.notes, notes) {
		t.Errorf("writable %v\nread-only %v\nnotes %q; want %v\n%v\n%q", p.writable, p.readOnly, p.notes, wantW, wantR, notes)
	}
}

// An unset, relative, missing, root or system-directory HOME or temporary
// directory is skipped with a note naming the variable and the value, and
// the other one stays writable.
func TestBuildRulesSkipsAnUnusableHomeOrTempDir(t *testing.T) {
	d := dirs(t, "w", "h", "t")
	usr := filepath.Join(d["base"], "usr")
	if err := os.Symlink("/usr", usr); err != nil {
		t.Fatal(err)
	}
	x := filepath.Join(d["base"], "missing")
	etc := resolved("/etc")[0] // a symlink to /private/etc on macOS, /etc itself on Linux
	for _, c := range []struct {
		env         []string
		note, other string
	}{
		{[]string{"TMPDIR=" + d["t"]}, `HOME="" is unset; not writable`, d["t"]},
		{[]string{"HOME=rel/home", "TMPDIR=" + d["t"]}, `HOME="rel/home" is relative; not writable`, d["t"]},
		{[]string{"HOME=/", "TMPDIR=" + d["t"]}, `HOME="/" resolves to /, which is or contains the system directory /usr; not writable`, d["t"]},
		{[]string{"HOME=" + usr, "TMPDIR=" + d["t"]}, `HOME="` + usr + `" resolves to /usr, which is or contains the system directory /usr; not writable`, d["t"]},
		{[]string{"HOME=" + d["h"], "TMPDIR=/etc"}, `TMPDIR="/etc" resolves to ` + etc + `, which is or contains the system directory /etc; not writable`, d["h"]},
		{[]string{"HOME=" + d["h"], "TEMP=" + x}, `TEMP="` + x + `" does not exist; not writable`, d["h"]},
	} {
		p, err := buildRules(Inputs{Workdir: d["w"], Env: c.env}, 3)
		if err != nil {
			t.Errorf("%v: %v", c.env, err)
			continue
		}
		if want := resolved(d["w"], c.other); !slices.Equal(p.notes, []string{c.note}) || !slices.Equal(p.writable[:2], want) ||
			slices.ContainsFunc(p.writable[2:], func(s string) bool { return !strings.HasPrefix(s, "/dev/") }) {
			t.Errorf("%v: notes %q, writable %v; want %q and %v", c.env, p.notes, p.writable, c.note, want)
		}
	}
}

// A missing workspace, or a root that cannot be resolved for any reason but
// absence, is an error naming the path.
func TestBuildRulesFailsOnAnUnresolvableRoot(t *testing.T) {
	d := dirs(t, "w")
	file := filepath.Join(d["base"], "f")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(file, "x") // ENOTDIR, not absence
	for _, c := range []struct {
		in   Inputs
		sys  string
		want string
	}{
		{Inputs{Workdir: filepath.Join(d["base"], "gone")}, "", "the workspace " + filepath.Join(d["base"], "gone") + " does not exist"},
		{Inputs{Workdir: bad}, "", "resolving " + bad},
		{Inputs{Workdir: d["w"], Env: []string{"HOME=" + bad}}, "", "HOME=" + bad},
		{Inputs{Workdir: d["w"], RW: []string{bad}}, "", "resolving " + bad},
		{Inputs{Workdir: d["w"], RO: []string{bad}}, "", "resolving " + bad},
		{Inputs{Workdir: d["w"]}, bad, "resolving " + bad},
	} {
		if c.sys != "" {
			extendLists(t, []string{c.sys}, nil)
		}
		if _, err := buildRules(c.in, 4); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v, want an error containing %q", c.in, err, c.want)
		}
	}
}
