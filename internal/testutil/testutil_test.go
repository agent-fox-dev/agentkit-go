package testutil

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRepoRootHoldsTheRootModule(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(RepoRoot(t), "go.mod"))
	if err != nil || !bytes.HasPrefix(raw, []byte("module "+rootModule+"\n")) {
		t.Fatalf("RepoRoot's go.mod: %q, %v", raw, err)
	}
}

func TestTreeWritesNestedFiles(t *testing.T) {
	dir := Tree(t, map[string]string{"a.txt": "A", "sub/deep/b.txt": "B"})
	for rel, want := range map[string]string{"a.txt": "A", "sub/deep/b.txt": "B"} {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", rel, got, err, want)
		}
	}
}

func TestHermeticPointsGitAtAnEmptyConfiguration(t *testing.T) {
	Hermetic(t)
	cfg, err := os.ReadFile(os.Getenv("GIT_CONFIG_GLOBAL"))
	if err != nil || len(cfg) != 0 || os.Getenv("GIT_CONFIG_NOSYSTEM") != "1" {
		t.Fatalf("GIT_CONFIG_GLOBAL holds %q (%v), GIT_CONFIG_NOSYSTEM = %q", cfg, err, os.Getenv("GIT_CONFIG_NOSYSTEM"))
	}
}

func TestWithinReportsWhetherTheConditionHeld(t *testing.T) {
	n := 0
	if !Within(time.Second, func() bool { n++; return n == 3 }) {
		t.Error("a condition that comes true was reported false")
	}
	if Within(20*time.Millisecond, func() bool { return false }) {
		t.Error("a condition that never holds was reported true")
	}
}

func TestJSONHelpers(t *testing.T) {
	m := Object(t, []byte(`{"n": 12345678901234567890, "s": "x"}`))
	if n, ok := m["n"].(json.Number); !ok || n.String() != "12345678901234567890" {
		t.Errorf(`Object(...)["n"] = %#v, want the literal as a json.Number`, m["n"])
	}
	if got := MustJSON(t, map[string]int{"a": 1}); got != `{"a":1}` {
		t.Errorf("MustJSON = %s", got)
	}
	for _, c := range []struct {
		a, b string
		same bool
	}{
		{`{"a":1,"b":[1,2]}`, `{ "b": [1, 2], "a": 1 }`, true},
		{`{"a":1}`, `{"a":2}`, false},
		{`{"a":1}`, `not json`, false},
	} {
		if got := SameJSON([]byte(c.a), []byte(c.b)); got != c.same {
			t.Errorf("SameJSON(%s, %s) = %v", c.a, c.b, got)
		}
	}
}

func TestTarHoldsTheFilesInNameOrder(t *testing.T) {
	tr := tar.NewReader(bytes.NewReader(Tar(t, map[string]string{"b": "2", "a/x": "1"})))
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(tr)
		names = append(names, h.Name+"="+string(body))
	}
	if got := strings.Join(names, " "); got != "a/x=1 b=2" {
		t.Errorf("entries = %s", got)
	}
}

func TestGoldenAcceptsTheSameBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.golden")
	if err := os.WriteFile(path, []byte("same\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	Golden(t, path, []byte("same\n"))
}

func TestGoRunReportsOutputAndExitCode(t *testing.T) {
	dir := Tree(t, map[string]string{
		"go.mod":  "module probe\n\ngo 1.27\n",
		"main.go": "package main\n\nimport (\"fmt\"; \"os\")\n\nfunc main() { fmt.Print(os.Getenv(\"PROBE\")); os.Exit(3) }\n",
	})
	env := Environ("PROBE", "GOFLAGS")
	env["PROBE"] = "seen"
	res := GoRun(t, dir, ".", env)
	if res.Stdout != "seen" || res.Exit != 1 || !strings.Contains(res.Stderr, "exit status 3") {
		t.Errorf("GoRun = %+v, want stdout %q, exit 1 and the program's status on stderr", res, "seen")
	}
}
