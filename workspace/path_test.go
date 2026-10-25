package workspace

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveContainsAPathInsideTheRoot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	for _, d := range []string{"a", "inner"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "a", "b.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, "a", filepath.Join(root, "link"))
	mustSymlink(t, "inner/new", filepath.Join(root, "dangling"))
	ws := open(t, root)
	r := ws.Root()

	for _, c := range []struct{ name, in, want string }{
		{"relative file", "a/b.txt", filepath.Join(r, "a", "b.txt")},
		{"absolute file", filepath.Join(r, "a", "b.txt"), filepath.Join(r, "a", "b.txt")},
		{"a file not created yet", "a/new/deeper/f.txt", filepath.Join(r, "a", "new", "deeper", "f.txt")},
		{"@ prefix", "@a/b.txt", filepath.Join(r, "a", "b.txt")},
		{"quoted", `"a/b.txt"`, filepath.Join(r, "a", "b.txt")},
		{"file URL, unwrapped before it is joined", "file://" + filepath.Join(r, "a", "b.txt"), filepath.Join(r, "a", "b.txt")},
		{"through an inside symlink", "link/b.txt", filepath.Join(r, "a", "b.txt")},
		{"dangling link to inside, as its target", "dangling", filepath.Join(r, "inner", "new")},
		{"dot-dot that stays inside", "a/../a/b.txt", filepath.Join(r, "a", "b.txt")},
		{"the root", ".", r},
	} {
		got, err := ws.Resolve(c.in)
		if err != nil || got != c.want {
			t.Errorf("%s: Resolve(%q) = %q, %v; want %q", c.name, c.in, got, err, c.want)
		}
	}
}

func TestResolveRefusesAPathOutsideTheRoot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	outside := t.TempDir()
	for _, d := range []string{root, root + "-evil"} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mustSymlink(t, outside, filepath.Join(root, "out"))
	mustSymlink(t, filepath.Join(outside, "newfile"), filepath.Join(root, "escape"))
	mustSymlink(t, filepath.Join(outside, "deep"), filepath.Join(root, "hop2"))
	mustSymlink(t, "hop2", filepath.Join(root, "hop1"))
	ws := open(t, root)

	for _, p := range []string{
		"../outside.txt", "a/../../outside.txt", "../..", "/etc/passwd",
		filepath.Join(outside, "f"),
		ws.Root() + "-evil/x", // shares the root's string prefix, not its segments
		"out/f",               // through a link to outside
		"escape",              // a dangling link to outside
		"hop1",                // a chain of dangling links that ends outside
	} {
		_, err := ws.Resolve(p)
		if !errors.Is(err, ErrPathNotAllowed) {
			t.Errorf("Resolve(%q) err = %v, want ErrPathNotAllowed", p, err)
			continue
		}
		msg := err.Error()
		if !strings.HasPrefix(msg, "workspace: path_not_allowed") || !strings.Contains(msg, ws.Root()) || !strings.HasSuffix(msg, pathNextStep) {
			t.Errorf("Resolve(%q): %q does not name the sentinel, the root and the next step", p, msg)
		}
	}
}

func TestResolveRefusesAMalformedPath(t *testing.T) {
	ws := open(t, t.TempDir())
	// hop0 -> hop1 -> ... -> hop40 -> missing: more links than are followed.
	for i := 0; i <= maxSymlinkHops; i++ {
		target := fmt.Sprintf("hop%d", i+1)
		if i == maxSymlinkHops {
			target = "missing"
		}
		mustSymlink(t, target, filepath.Join(ws.Root(), fmt.Sprintf("hop%d", i)))
	}
	mustSymlink(t, "cycle", filepath.Join(ws.Root(), "cycle"))

	for _, p := range []string{"", "   ", "a\x00b", "file://host/x", "~no-such-user-agentkit/x", "hop0", "cycle"} {
		_, err := ws.Resolve(p)
		if !errors.Is(err, ErrPathMalformed) || !strings.HasPrefix(err.Error(), "workspace: path_malformed") {
			t.Errorf("Resolve(%q) err = %v, want ErrPathMalformed", p, err)
		}
	}
}

func TestNormalizeRewritesWhatAModelEmits(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cases := []struct{ in, want string }{
		{"~", home},
		{"~/x", filepath.Join(home, "x")},
		{"@x", "x"},
		{`"quoted path"`, "quoted path"},
		{`'single'`, "single"},
		{"  padded  ", "padded"},
		{"file:///abs/x", "/abs/x"},
		{"plain/rel", "plain/rel"},
	}
	if me, err := user.Current(); err == nil && me.HomeDir != "" {
		cases = append(cases, struct{ in, want string }{"~" + me.Username + "/x", filepath.Join(me.HomeDir, "x")})
	}
	for _, c := range cases {
		if got, err := Normalize(c.in); err != nil || got != c.want {
			t.Errorf("Normalize(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
}

func TestNormalizeRefusesTildeWithoutAHome(t *testing.T) {
	t.Setenv("HOME", "")
	for _, p := range []string{"~", "~/x"} {
		got, err := Normalize(p)
		if !errors.Is(err, ErrNoHome) || got != "" {
			t.Errorf("Normalize(%q) = %q, %v; want ErrNoHome", p, got, err)
		}
	}
	if _, err := open(t, t.TempDir()).Resolve("~/x"); !errors.Is(err, ErrNoHome) {
		t.Errorf("Resolve(~/x) err = %v, want ErrNoHome", err)
	}
}

func TestRelIsRootRelativeOrTheInputUnchanged(t *testing.T) {
	ws := open(t, t.TempDir())
	r := ws.Root()
	for _, c := range []struct{ in, want string }{
		{filepath.Join(r, "a", "b"), filepath.Join("a", "b")},
		{r, "."},
		{"/elsewhere", "/elsewhere"},
		{"relative", "relative"},
	} {
		if got := ws.Rel(c.in); got != c.want {
			t.Errorf("Rel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCheckWriteTargetAllowsAPlainTargetInside(t *testing.T) {
	ws := open(t, t.TempDir())
	if err := os.WriteFile(filepath.Join(ws.Root(), "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"f", "absent", "new/dir/file"} {
		if err := ws.CheckWriteTarget(filepath.Join(ws.Root(), rel)); err != nil {
			t.Errorf("CheckWriteTarget(%s): %v", rel, err)
		}
	}
}

// A link planted after Resolve, at the leaf or at a parent directory, is
// caught by the check made just before the write.
func TestCheckWriteTargetRefusesALinkPlantedAfterResolve(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "file.txt"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws := open(t, t.TempDir())
	leaf := filepath.Join(ws.Root(), "target.txt")
	sub := filepath.Join(ws.Root(), "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	inSub := filepath.Join(sub, "file.txt")
	for _, abs := range []string{leaf, inSub} {
		if err := ws.CheckWriteTarget(abs); err != nil {
			t.Fatalf("before the swap, %s: %v", abs, err)
		}
	}
	mustSymlink(t, filepath.Join(outside, "x"), leaf)
	if err := os.Remove(sub); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, outside, sub)

	for abs, says := range map[string]string{leaf: "is a symlink whose target", inSub: "now resolves to"} {
		err := ws.CheckWriteTarget(abs)
		if !errors.Is(err, ErrPathNotAllowed) || !strings.Contains(err.Error(), says) || !strings.HasSuffix(err.Error(), pathNextStep) {
			t.Errorf("CheckWriteTarget(%s) err = %v, want ErrPathNotAllowed saying %q", abs, err, says)
		}
	}
	mustSymlink(t, "loop", filepath.Join(ws.Root(), "loop"))
	if err := ws.CheckWriteTarget(filepath.Join(ws.Root(), "loop")); !errors.Is(err, ErrPathMalformed) {
		t.Errorf("CheckWriteTarget of a link loop: err = %v, want ErrPathMalformed", err)
	}
}
