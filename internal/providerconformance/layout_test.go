package providerconformance

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/agent-fox-dev/agentkit-go"

// repoRoot is the directory of the root module: the nearest ancestor of the
// test's working directory whose go.mod names it.
func repoRoot(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if raw, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil &&
			strings.HasPrefix(string(raw), "module "+modulePath+"\n") {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod of %s above the working directory", modulePath)
		}
		dir = parent
	}
}

// goFiles lists the Go files under dir, nested modules, testdata and dot
// directories left out. withTests says whether _test.go files are listed.
func goFiles(t testing.TB, dir string, withTests bool) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == dir {
				return nil
			}
			if n := d.Name(); n == "testdata" || n == "build" || strings.HasPrefix(n, ".") {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && (withTests || !strings.HasSuffix(path, "_test.go")) {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// listenCalls names every socket-listening function file refers to through its
// own import names: net.Listen*, tls.Listen and http.ListenAndServe*.
func listenCalls(t testing.TB, file string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	local := map[string]string{} // local import name -> import path
	for _, imp := range f.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		name := path[strings.LastIndex(path, "/")+1:]
		if imp.Name != nil {
			name = imp.Name.Name
		}
		local[name] = path
	}
	var found []string
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		switch path := local[id.Name]; {
		case path == "net" && strings.HasPrefix(sel.Sel.Name, "Listen"),
			path == "crypto/tls" && sel.Sel.Name == "Listen",
			path == "net/http" && strings.HasPrefix(sel.Sel.Name, "ListenAndServe"):
			found = append(found, path+"."+sel.Sel.Name)
		}
		return true
	})
	return found
}

// importsOf lists the import paths of file.
func importsOf(t testing.TB, file string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		paths = append(paths, p)
	}
	return paths
}

// TS-30-1 (30-REQ-1.1): every line of code in the package is in a _test.go
// file, doc.go holds only the package comment, no non-test file of the root
// module listens on a socket, and no package imports this one.
func TestOnlyTestFilesHoldCode_TS30_1(t *testing.T) {
	root := repoRoot(t)

	all, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var nonTest []string
	for _, f := range all {
		if !strings.HasSuffix(f, "_test.go") {
			nonTest = append(nonTest, f)
		}
	}
	if !slices.Equal(nonTest, []string{"doc.go"}) {
		t.Fatalf("non-test files = %v, want only doc.go", nonTest)
	}
	doc, err := parser.ParseFile(token.NewFileSet(), "doc.go", nil, parser.ParseComments|parser.PackageClauseOnly)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Doc == nil || strings.TrimSpace(doc.Doc.Text()) == "" {
		t.Error("doc.go has no package doc comment")
	}
	if full, err := parser.ParseFile(token.NewFileSet(), "doc.go", nil, 0); err != nil {
		t.Fatal(err)
	} else if len(full.Decls) != 0 {
		t.Errorf("doc.go declares %d things, want only the package clause and its comment", len(full.Decls))
	}

	for _, f := range goFiles(t, root, false) {
		if calls := listenCalls(t, f); len(calls) != 0 {
			t.Errorf("%s listens on a socket (%v); only a _test.go file may", f, calls)
		}
	}

	self := modulePath + "/internal/providerconformance"
	for _, f := range goFiles(t, root, true) {
		if filepath.Dir(f) == filepath.Join(root, "internal", "providerconformance") {
			continue
		}
		if slices.Contains(importsOf(t, f), self) {
			t.Errorf("%s imports %s; nothing may", f, self)
		}
	}
}

// The listener scan is the check TS-30-1 stands on, so it must see what it
// looks for, through an alias too, and miss a name that only looks alike.
func TestListenScanSeesListeners_TS30_1(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for _, c := range []struct{ src, want string }{
		{"package x\nimport \"net\"\nvar _ = net.Listen\n", "net.Listen"},
		{"package x\nimport n \"net\"\nvar _ = n.ListenTCP\n", "net.ListenTCP"},
		{"package x\nimport \"crypto/tls\"\nvar _ = tls.Listen\n", "crypto/tls.Listen"},
		{"package x\nimport \"net/http\"\nvar _ = http.ListenAndServeTLS\n", "net/http.ListenAndServeTLS"},
	} {
		if got := listenCalls(t, write("x.go", c.src)); !slices.Equal(got, []string{c.want}) {
			t.Errorf("%q: found %v, want %v", c.src, got, c.want)
		}
	}
	if got := listenCalls(t, write("y.go", "package y\nimport \"net/http\"\nvar _ = http.NewServeMux\nvar Listen = 1\n")); len(got) != 0 {
		t.Errorf("a name that only looks alike was reported: %v", got)
	}
}

// liveCommand is the command make provider-live-test runs.
const liveCommand = "AGENTKIT_LIVE=1 go test -count=1 -v -timeout 30m -run TestLive ./internal/providerconformance"

// prerequisites reads the Makefile's rules into target -> prerequisites. A line
// is a rule when it names targets before a colon that is not part of :=.
func prerequisites(t testing.TB, makefile string) map[string][]string {
	t.Helper()
	rules := map[string][]string{}
	for _, line := range strings.Split(makefile, "\n") {
		if line == "" || line[0] == '\t' || line[0] == '#' || line[0] == ' ' {
			continue
		}
		head, rest, ok := strings.Cut(line, ":")
		if !ok || strings.HasPrefix(rest, "=") || strings.ContainsAny(head, "=$ ") {
			continue
		}
		rest, _, _ = strings.Cut(rest, "#")
		rules[head] = append(rules[head], strings.Fields(rest)...)
	}
	return rules
}

// closure is target and everything it depends on, transitively.
func closure(rules map[string][]string, target string) map[string]bool {
	seen := map[string]bool{}
	var walk func(string)
	walk = func(n string) {
		if seen[n] {
			return
		}
		seen[n] = true
		for _, p := range rules[n] {
			walk(p)
		}
	}
	walk(target)
	return seen
}

// recipe is the tab-indented lines under the rule for target.
func recipe(makefile, target string) []string {
	var out []string
	in := false
	for _, line := range strings.Split(makefile, "\n") {
		switch {
		case strings.HasPrefix(line, target+":"):
			in = true
		case in && strings.HasPrefix(line, "\t"):
			out = append(out, strings.TrimPrefix(line, "\t"))
		case in && line != "" && !strings.HasPrefix(line, "#"):
			return out
		}
	}
	return out
}

// TS-30-33 (30-REQ-6.7): make provider-live-test runs the live command and
// nothing else, and neither check nor check-release depends on it.
func TestProviderLiveTestTarget_TS30_33(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not on PATH")
	}
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	makefile := string(raw)

	dry := exec.Command("make", "-n", "provider-live-test")
	dry.Dir = root
	out, err := dry.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n provider-live-test: %v\n%s", err, out)
	}
	// The dry run prints the command as it would run, and prints nothing else:
	// a wrapper that discards the exit status would show up as more text.
	if got := strings.TrimSpace(string(out)); got != liveCommand {
		t.Errorf("make -n provider-live-test prints\n%s\nwant exactly\n%s", got, liveCommand)
	}
	lines := recipe(makefile, "provider-live-test")
	if len(lines) != 1 || lines[0] != liveCommand {
		t.Errorf("the recipe is %q, want the one line %q: a prefix such as - or @- would discard the exit status", lines, liveCommand)
	}

	rules := prerequisites(t, makefile)
	if _, ok := rules["provider-live-test"]; !ok {
		t.Fatal("the Makefile has no provider-live-test rule")
	}
	for _, target := range []string{"check", "check-release"} {
		if closure(rules, target)["provider-live-test"] {
			t.Errorf("provider-live-test is a prerequisite of %s, directly or through another target", target)
		}
		dry := exec.Command("make", "-n", target)
		dry.Dir = root
		out, err := dry.CombinedOutput()
		if err != nil {
			t.Fatalf("make -n %s: %v\n%s", target, err, out)
		}
		if strings.Contains(string(out), "TestLive") || strings.Contains(string(out), "AGENTKIT_LIVE") {
			t.Errorf("make -n %s would run the live tests:\n%s", target, out)
		}
	}
	if !slices.Contains(strings.Fields(strings.Join(grepLines(makefile, ".PHONY:"), " ")), "provider-live-test") {
		t.Error("provider-live-test is not declared .PHONY")
	}
}

// grepLines returns the lines of s that start with prefix.
func grepLines(s, prefix string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return out
}

// The Makefile reader is what TS-30-33 stands on: a dependency through another
// target counts, and a recipe with a discarding prefix is seen as different.
func TestMakefileReader_TS30_33(t *testing.T) {
	mf := "CMD := x\n.PHONY: a\na: b ## doc\n\tcmd1\nb: c\nc:\n\t-AGENTKIT_LIVE=1 go test\n\n# note\nd: a\n"
	rules := prerequisites(t, mf)
	if got := closure(rules, "d"); !got["a"] || !got["b"] || !got["c"] || got["CMD"] {
		t.Errorf("closure of d = %v, want a, b and c", got)
	}
	if got := recipe(mf, "c"); !slices.Equal(got, []string{"-AGENTKIT_LIVE=1 go test"}) {
		t.Errorf("recipe of c = %q", got)
	}
}
