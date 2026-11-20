package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
)

// searchRequest is a request file for a code_search of query.
func searchRequest(t *testing.T, query string) string {
	t.Helper()
	return writeRequest(t, fmt.Sprintf(`{"name":"code_search","input":{"query":%q}}`, query))
}

// manifests are the store manifests under indexDir.
func manifests(indexDir string) []string {
	m, _ := filepath.Glob(filepath.Join(indexDir, "agentkit-codesearch-*", "manifest.json"))
	return m
}

// snapshot is every file under dir with its size and modification time.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() && fi.Name() != "lock" {
			rel, _ := filepath.Rel(dir, p)
			out[rel] = fmt.Sprintf("%d %d", fi.Size(), fi.ModTime().UnixNano())
		}
		return nil
	})
	return out
}

// needSearch skips a test where the platform serves no code_search.
func needSearch(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("zoekt does not run on windows")
	}
}

func TestTheIndexDirComesFromTheFlagThenTheVariable(t *testing.T) {
	needSearch(t)
	dir := sampleTree(t)
	for _, c := range []struct {
		name  string
		flag  *string
		env   bool
		where string // "flag", "env"
	}{
		{"the flag alone, its directory missing", new("flag"), false, "flag"},
		{"the variable alone", nil, true, "env"},
		{"both: the flag wins", new("flag"), true, "flag"},
		{"an empty flag and the variable", new(""), true, "env"},
	} {
		t.Run(c.name, func(t *testing.T) {
			base := t.TempDir()
			flagDir, envDir := filepath.Join(base, "flag", "missing"), filepath.Join(base, "env")
			args := []string{"call", "--dir", dir, "--request", searchRequest(t, "needle")}
			if c.flag != nil {
				v := ""
				if *c.flag == "flag" {
					v = flagDir
				}
				args = append(args, "--index-dir", v)
			}
			env := testEnv()
			if c.env {
				env = testEnv("AGENTKIT_INDEX_DIR=" + envDir)
			}
			res := runIn(t, "", env, args...)
			if res.exit != 0 || !parseCall(t, res.stdout).OK {
				t.Fatalf("exit %d, stdout %s, stderr %s", res.exit, res.stdout, res.stderr)
			}
			want, other := flagDir, envDir
			if c.where == "env" {
				want, other = envDir, flagDir
			}
			if len(manifests(want)) != 1 {
				t.Errorf("no stored index under %s: %v", want, manifests(want))
			}
			if len(manifests(other)) != 0 {
				t.Errorf("a stored index under %s", other)
			}
		})
	}
}

func TestWithoutAnIndexDirTheIndexLeavesNothingBehind(t *testing.T) {
	needSearch(t)
	tmp := t.TempDir()
	res := runIn(t, "", testEnv("TMPDIR="+tmp), "call", "--dir", sampleTree(t), "--request", searchRequest(t, "needle"))
	if res.exit != 0 || !parseCall(t, res.stdout).OK {
		t.Fatalf("exit %d, stdout %s, stderr %s", res.exit, res.stdout, res.stderr)
	}
	if left, _ := filepath.Glob(filepath.Join(tmp, "agentkit-codesearch-*")); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
}

func TestAnIndexDirInsideTheServedDirectoryIsRefused(t *testing.T) {
	dir := sampleTree(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, dir)
	req := searchRequest(t, "needle")
	for _, c := range []struct {
		name  string
		flags []string
		env   []string
	}{
		{"the served directory", []string{"--index-dir", dir}, nil},
		{"under it", []string{"--index-dir", filepath.Join(dir, ".index")}, nil},
		{"the variable under it", nil, []string{"AGENTKIT_INDEX_DIR=" + filepath.Join(dir, "x")}},
		{"a link into it", []string{"--index-dir", filepath.Join(link, "y")}, nil},
	} {
		for _, sub := range [][]string{{"call", "--request", req}, {"tools"}, {"mcp"}} {
			t.Run(c.name+"/"+sub[0], func(t *testing.T) {
				args := append(append(slices.Clone(sub), "--dir", dir), c.flags...)
				res := runIn(t, "", testEnv(c.env...), args...)
				if res.exit != 2 || res.stdout != "" {
					t.Fatalf("exit %d, stdout %q", res.exit, res.stdout)
				}
				if l := lines(res.stderr); len(l) != 1 || !strings.Contains(l[0], "outside") {
					t.Fatalf("stderr = %q, want one line saying it must be outside", res.stderr)
				}
			})
		}
	}
	if after := snapshot(t, dir); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatalf("the served directory changed: %v, was %v", after, before)
	}
}

func TestAnIndexDirThatCannotBeWrittenFallsBackToATemporaryIndex(t *testing.T) {
	needSearch(t)
	if os.Geteuid() == 0 {
		t.Skip("root writes through file modes")
	}
	base := t.TempDir()
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	readOnly := filepath.Join(base, "ro")
	if err := os.Mkdir(readOnly, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(readOnly, 0o700) })
	dir := sampleTree(t)
	for _, x := range []string{filepath.Join(file, "below"), readOnly} {
		t.Run(filepath.Base(x), func(t *testing.T) {
			res := run(t, "call", "--dir", dir, "--index-dir", x, "--request", searchRequest(t, "needle"))
			if res.exit != 0 || !parseCall(t, res.stdout).OK {
				t.Fatalf("exit %d, stdout %s, stderr %s", res.exit, res.stdout, res.stderr)
			}
			if l := lines(res.stderr); len(l) != 1 || !strings.Contains(l[0], x) {
				t.Fatalf("stderr = %q, want one line naming %s", res.stderr, x)
			}
		})
	}
}

func TestASingleCallWithAStoredIndexIsAFifthOfTheBuild(t *testing.T) {
	needSearch(t)
	if testing.Short() {
		t.Skip("generates 10 000 files and times two calls; make check-release runs it")
	}
	files := map[string]string{"go.mod": "module big\n\ngo 1.27\n"}
	for i := range 10000 {
		files[fmt.Sprintf("p%02d/f%05d.go", i%100, i)] = fmt.Sprintf("package p%02d\n\n// F%05d mentions needle.\nfunc F%05d() int { return %d }\n", i%100, i, i, i)
	}
	dir := testutil.Tree(t, files)
	// A file indexed within the 2 s timestamp granule of its last write is
	// read and hashed again by the next process, so a just-written tree would
	// time that check and not the reuse: the tree is dated an hour back, as a
	// checkout nobody is writing is.
	settled := time.Now().Add(-time.Hour)
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			_ = os.Chtimes(p, settled, settled)
		}
		return nil
	})
	indexDir := t.TempDir()
	timed := func() (time.Duration, callResult) {
		start := time.Now()
		res := run(t, "call", "--dir", dir, "--index-dir", indexDir, "--request", searchRequest(t, "F04242"))
		elapsed := time.Since(start)
		if res.exit != 0 {
			t.Fatalf("exit %d, stderr %s", res.exit, res.stderr)
		}
		return elapsed, parseCall(t, res.stdout)
	}
	t1, r1 := timed()
	t2, r2 := timed()
	if !r1.OK || !r2.OK || r1.Text != r2.Text {
		t.Fatalf("the two calls differ: %v %v", r1.OK, r2.OK)
	}
	if t2*5 > t1 {
		t.Fatalf("the call with a stored index took %v, more than a fifth of the building call's %v", t2, t1)
	}
}

func TestARelayReusesTheIndexAcrossSingleCalls(t *testing.T) {
	needSearch(t)
	dir := sampleTree(t)
	indexDir := t.TempDir()
	req := searchRequest(t, "needle")
	r1 := run(t, "call", "--dir", dir, "--index-dir", indexDir, "--request", req)
	if r1.exit != 0 {
		t.Fatalf("first call: exit %d, stderr %s", r1.exit, r1.stderr)
	}
	stored := snapshot(t, indexDir)
	r2 := run(t, "call", "--dir", dir, "--index-dir", indexDir, "--request", req)
	if r2.exit != 0 {
		t.Fatalf("second call: exit %d, stderr %s", r2.exit, r2.stderr)
	}
	c1, c2 := parseCall(t, r1.stdout), parseCall(t, r2.stdout)
	if !c1.OK || !c2.OK || c1.Text != c2.Text || !strings.Contains(c1.Text, "a.go") {
		t.Fatalf("results differ or miss a.go:\n%s\n%s", c1.Text, c2.Text)
	}
	if len(manifests(indexDir)) != 1 {
		t.Fatalf("manifests = %v, want one", manifests(indexDir))
	}
	if after := snapshot(t, indexDir); fmt.Sprint(after) != fmt.Sprint(stored) {
		t.Fatalf("the second call rewrote the store: %v, was %v", after, stored)
	}
}

func TestASessionsEditIsSeenByALaterSingleCall(t *testing.T) {
	needSearch(t)
	dir := sampleTree(t)
	// Enough files that one edit stays under the 5% that triggers a rebuild.
	for i := range 40 {
		testutil.WriteTree(t, dir, map[string]string{fmt.Sprintf("more/m%02d.go", i): fmt.Sprintf("package more\n\nfunc M%02d() {}\n", i)})
	}
	indexDir := t.TempDir()
	s := startSession(t, "--dir", dir, "--index-dir", indexDir)
	if got := s.call(t, "code_search", map[string]any{"query": "needle"}); !strings.Contains(got, "a.go") {
		t.Fatalf("the session's search: %s", got)
	}
	s.call(t, "edit_file", map[string]any{"path": "b.go", "edits": []any{map[string]any{
		"old_string": "func Other() int { return 2 }", "new_string": "func Other() int { return 2 } // fresh_marker"}}})
	if err := s.conn.Close(); err != nil {
		t.Fatal(err)
	}
	if code := waitExit(t, s); code != 0 {
		t.Fatalf("the session exited %d; stderr %s", code, s.stderr)
	}
	stored := snapshot(t, indexDir)
	res := run(t, "call", "--dir", dir, "--index-dir", indexDir, "--request", searchRequest(t, "fresh_marker"))
	if got := parseCall(t, res.stdout); res.exit != 0 || !got.OK || !strings.Contains(got.Text, "b.go") {
		t.Fatalf("the later call: exit %d, %s", res.exit, res.stdout)
	}
	if after := snapshot(t, indexDir); fmt.Sprint(after) != fmt.Sprint(stored) {
		t.Fatalf("the later call rebuilt the store: %v, was %v", after, stored)
	}
}

func TestTwoSingleCallsAtOnceStoreOneIndex(t *testing.T) {
	needSearch(t)
	dir := sampleTree(t)
	indexDir := t.TempDir()
	req := searchRequest(t, "needle")
	p1 := start(t, testEnv(), "call", "--dir", dir, "--index-dir", indexDir, "--request", req)
	p2 := start(t, testEnv(), "call", "--dir", dir, "--index-dir", indexDir, "--request", req)
	for i, p := range []*started{p1, p2} {
		if code := p.wait(time.Minute); code != 0 {
			t.Fatalf("call %d: exit %d, stderr %s", i, code, p.stderr)
		}
	}
	c1, c2 := parseCall(t, p1.stdout.String()), parseCall(t, p2.stdout.String())
	if !c1.OK || !c2.OK || c1.Text != c2.Text {
		t.Fatalf("the two results differ:\n%s\n%s", c1.Text, c2.Text)
	}
	if len(manifests(indexDir)) != 1 {
		t.Fatalf("manifests = %v, want one", manifests(indexDir))
	}
	for f := range snapshot(t, indexDir) {
		if strings.Contains(f, ".tmp") {
			t.Errorf("a temporary name is left: %s", f)
		}
	}
}
