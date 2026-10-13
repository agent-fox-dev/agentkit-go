package confine

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// resolved is p with symlinks resolved, or "" when it does not exist.
func resolved(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return ""
	}
	return r
}

// existing is the resolved, duplicate-free members of paths that exist.
func existing(t *testing.T, paths []string, seen map[string]bool) []string {
	t.Helper()
	var out []string
	for _, p := range paths {
		if r := resolved(t, p); r != "" && !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	return out
}

func mkdirs(t *testing.T, n int) []string {
	t.Helper()
	out := make([]string, n)
	for i := range out {
		out[i] = t.TempDir()
	}
	return out
}

// TS-15-1: the rule builder grants every right beneath the writable roots,
// read and execute on the system and --landlock-ro roots, read only on /proc,
// read and write on the device files, and nothing else. 15-REQ-1.1,
// 15-REQ-1.2.
func TestRuleRights_TS15_1(t *testing.T) {
	d := mkdirs(t, 5)
	w, h, tmp, r, o := d[0], d[1], d[2], d[3], d[4]
	in := Inputs{Workdir: w, Env: []string{"HOME=" + h, "TMPDIR=" + tmp}, RW: []string{r}, RO: []string{o}, Mode: sandbox.NetworkUnrestricted}
	for _, abi := range []int{2, 3, 4} {
		p, err := buildRules(in, abi)
		if err != nil {
			t.Fatalf("ABI %d: %v", abi, err)
		}
		seen := map[string]bool{}
		roots := existing(t, []string{w, h, tmp, r}, seen)
		devices := existing(t, deviceFiles, seen)
		system := existing(t, systemDirs, seen)
		ro := existing(t, []string{o}, seen)
		proc := existing(t, []string{"/proc"}, seen)
		if want := slices.Concat(roots, devices); !slices.Equal(p.writable, want) {
			t.Errorf("ABI %d: Writable %v, want %v", abi, p.writable, want)
		}
		if want := slices.Concat(system, ro, proc); !slices.Equal(p.readOnly, want) {
			t.Errorf("ABI %d: ReadOnly %v, want %v", abi, p.readOnly, want)
		}
		full := accExecute | accWriteFile | accReadFile | accReadDir | accRemoveDir | accRemoveFile | accMakeChar |
			accMakeDir | accMakeReg | accMakeSock | accMakeFifo | accMakeBlock | accMakeSym | accRefer
		if abi >= 3 {
			full |= accTruncate
		}
		devAccess := accReadFile | accWriteFile
		if abi >= 3 {
			devAccess |= accTruncate
		}
		want := map[string]access{}
		for _, p := range roots {
			want[p] = full
		}
		for _, p := range slices.Concat(system, ro) {
			want[p] = accReadFile | accReadDir | accExecute
		}
		for _, p := range proc {
			want[p] = accReadFile | accReadDir
		}
		for _, p := range devices {
			want[p] = devAccess
		}
		if len(p.rules) != len(want) {
			t.Errorf("ABI %d: %d rules, want %d", abi, len(p.rules), len(want))
		}
		for _, ru := range p.rules {
			acc, ok := want[ru.path]
			if !ok {
				t.Errorf("ABI %d: a rule names %s", abi, ru.path)
				continue
			}
			isDev := slices.Contains(devices, ru.path)
			if ru.access != acc || ru.dir == isDev {
				t.Errorf("ABI %d: rule %s grants %#x (dir %v), want %#x (dir %v)", abi, ru.path, ru.access, ru.dir, acc, !isDev)
			}
		}
		if fullFS(abi) != full {
			t.Errorf("fullFS(%d) = %#x, want %#x", abi, fullFS(abi), full)
		}
	}
}

// replaceLists extends the system and device lists for the test.
func replaceLists(t *testing.T, sys, dev []string) {
	t.Helper()
	oldS, oldD := systemDirs, deviceFiles
	systemDirs, deviceFiles = slices.Concat(oldS, sys), slices.Concat(oldD, dev)
	t.Cleanup(func() { systemDirs, deviceFiles = oldS, oldD })
}

// TS-15-4 (building half): missing system, device and flag paths are
// skipped, only the flag paths are noted, and the rest still applies.
// 15-REQ-1.5. The applying half is in setup_linux_test.go.
func TestMissingPathsSkipped_TS15_4(t *testing.T) {
	replaceLists(t, []string{"/nonexistent-agentkit-sys"}, []string{"/dev/nonexistent-agentkit"})
	base := t.TempDir()
	w, m1, m2 := t.TempDir(), filepath.Join(base, "m1"), filepath.Join(base, "m2")
	p, err := buildRules(Inputs{Workdir: w, RO: []string{m1}, RW: []string{m2}}, 3)
	if err != nil {
		t.Fatal(err)
	}
	all := slices.Concat(p.writable, p.readOnly)
	for _, missing := range []string{m1, m2, "/nonexistent-agentkit-sys", "/dev/nonexistent-agentkit"} {
		if slices.Contains(all, missing) {
			t.Errorf("%s is listed", missing)
		}
	}
	if !slices.Contains(p.writable, resolved(t, w)) || !slices.Contains(p.readOnly, resolved(t, "/usr")) {
		t.Errorf("an existing root is missing: %v %v", p.writable, p.readOnly)
	}
	note := strings.Join(p.notes, "; ")
	if !strings.Contains(note, m1) || !strings.Contains(note, m2) || strings.Contains(note, "nonexistent-agentkit") {
		t.Errorf("notes %q, want M1 and M2 and no system or device path", note)
	}
}

// TS-15-7: for any set of roots, the report lists resolved, duplicate-free
// paths in the order their rules were added. 15-REQ-1.8.
func TestRootsResolvedAndCollapsed_TS15_7(t *testing.T) {
	for seed := uint64(1); seed <= 60; seed++ {
		r := rand.New(rand.NewPCG(seed, 7))
		base := t.TempDir()
		var real, names []string
		for i := range 3 + r.IntN(4) {
			d := filepath.Join(base, fmt.Sprintf("d%d", i))
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
			real = append(real, d)
			names = append(names, d, d+"/", filepath.Join(base, "x", "..", fmt.Sprintf("d%d", i)))
		}
		_ = os.MkdirAll(filepath.Join(base, "x"), 0o755)
		for i, d := range real {
			l := filepath.Join(base, fmt.Sprintf("l%d", i))
			if err := os.Symlink(d, l); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			ll := filepath.Join(base, fmt.Sprintf("ll%d", i))
			_ = os.Symlink(l, ll) // a chain
			names = append(names, l, ll)
		}
		res := func(p string) string { return resolved(t, p) }
		// rw and ro inputs draw from disjoint halves of the real directories.
		half := len(real) / 2
		inHalf := func(p string, lo bool) bool {
			i := slices.Index(real, res(p))
			for j, d := range real {
				if res(d) == res(p) {
					i = j
				}
			}
			return (i < half) == lo
		}
		pick := func(lo bool) string {
			for {
				if n := names[r.IntN(len(names))]; inHalf(n, lo) {
					return n
				}
			}
		}
		in := Inputs{Workdir: pick(true), Env: []string{"HOME=" + pick(true), "TMPDIR=" + pick(true)}}
		for range r.IntN(5) {
			in.RW = append(in.RW, pick(true))
		}
		for range r.IntN(5) {
			in.RO = append(in.RO, pick(false))
		}
		p, err := buildRules(in, 4)
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		seen := map[string]bool{}
		wantW := slices.Concat(existing(t, slices.Concat([]string{in.Workdir, in.Env[0][5:], in.Env[1][7:]}, in.RW), seen),
			existing(t, deviceFiles, seen))
		wantR := slices.Concat(existing(t, systemDirs, seen), existing(t, in.RO, seen), existing(t, []string{"/proc"}, seen))
		if !slices.Equal(p.writable, wantW) || !slices.Equal(p.readOnly, wantR) {
			t.Fatalf("seed %d: inputs %+v gave\nWritable %v\nReadOnly %v\nwant %v\n%v", seed, in, p.writable, p.readOnly, wantW, wantR)
		}
		for _, e := range slices.Concat(p.writable, p.readOnly) {
			if res(e) != e {
				t.Errorf("seed %d: %s is not resolved", seed, e)
			}
		}
		if len(p.rules) != len(seen) || slices.Contains(p.writable, "/proc") {
			t.Errorf("seed %d: %d rules for %d distinct roots", seed, len(p.rules), len(seen))
		}
	}
}
