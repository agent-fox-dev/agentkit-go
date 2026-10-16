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

// nextStep ends every path_not_allowed message (20-REQ-2.2).
const nextStep = "; use a path inside the workspace (a relative path is resolved against the root)"

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}
}

// openDir opens a workspace over a fresh temp directory.
func openDir(t *testing.T) *Workspace {
	t.Helper()
	ws, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

func resolved(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	r, err := filepath.EvalSymlinks(abs)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TS-20-1: Open on an existing root returns its absolute, symlink-resolved path.
func TestOpenResolvesAnExistingRoot_TS20_1(t *testing.T) {
	target := t.TempDir()
	linkDir := t.TempDir()
	link := filepath.Join(linkDir, "link")
	mustSymlink(t, target, link)

	want := resolved(t, link)
	if want != resolved(t, target) {
		t.Fatalf("test setup: %q does not resolve to %q", link, target)
	}
	ws, err := Open(link, Options{})
	if err != nil {
		t.Fatalf("Open(%q): %v", link, err)
	}
	if ws.Root() != want {
		t.Fatalf("Root() = %q, want %q", ws.Root(), want)
	}

	t.Chdir(linkDir)
	rel, err := Open("link", Options{})
	if err != nil {
		t.Fatalf("Open(relative): %v", err)
	}
	if rel.Root() != want {
		t.Fatalf("relative Root() = %q, want %q", rel.Root(), want)
	}
}

// TS-20-2: Open with an empty root uses the symlink-resolved working directory.
func TestOpenEmptyRootUsesTheWorkingDirectory_TS20_2(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "cwd")
	mustSymlink(t, target, link)
	t.Chdir(link)

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := Open("", Options{})
	if err != nil {
		t.Fatalf("Open(\"\"): %v", err)
	}
	if ws.Root() != want {
		t.Fatalf("Root() = %q, want %q", ws.Root(), want)
	}
	if want != resolved(t, target) {
		t.Fatalf("Root() %q is not the symlink target %q", want, resolved(t, target))
	}
}

// TS-20-3: Open keeps a missing root in lexical absolute form without error.
func TestOpenKeepsAMissingRootLexical_TS20_3(t *testing.T) {
	base := t.TempDir()
	missing := filepath.Join(base, "does", "not", "exist")
	want, err := filepath.Abs(missing)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := Open(missing, Options{})
	if err != nil {
		t.Fatalf("Open(missing): %v", err)
	}
	if ws.Root() != want {
		t.Fatalf("Root() = %q, want the lexical %q", ws.Root(), want)
	}
	if _, err := os.Lstat(filepath.Join(base, "does")); err == nil {
		t.Fatal("Open created a directory")
	}

	t.Chdir(base)
	relWant, err := filepath.Abs("missing-rel")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := Open("missing-rel", Options{})
	if err != nil {
		t.Fatalf("Open(relative missing): %v", err)
	}
	if rel.Root() != relWant {
		t.Fatalf("relative Root() = %q, want %q", rel.Root(), relWant)
	}
}

// TS-20-4: Open returns (nil, error) when the working directory cannot be
// determined.
func TestOpenFailsWithoutAWorkingDirectory_TS20_4(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "gone")
	if err := os.Mkdir(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(gone)
	if err := os.Remove(gone); err != nil {
		t.Skipf("cannot remove the working directory here: %v", err)
	}
	if _, err := os.Getwd(); err == nil {
		t.Skip("os.Getwd still succeeds after the working directory is removed")
	}
	for _, root := range []string{"", "relative"} {
		ws, err := Open(root, Options{})
		if ws != nil {
			t.Errorf("Open(%q) returned a workspace", root)
		}
		if err == nil {
			t.Errorf("Open(%q) returned no error", root)
		}
	}
}

// TS-20-8: Resolve normalizes, joins, resolves symlinks against the deepest
// ancestor and compares segment-wise.
func TestResolveContainsInsidePaths_TS20_8(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	for _, d := range []string{filepath.Join(root, "a"), filepath.Join(root, "inner"), root + "-evil"} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "a", "b.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, "a", filepath.Join(root, "link"))
	mustSymlink(t, "inner/new", filepath.Join(root, "dangling"))
	ws, err := Open(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	r := ws.Root()

	for _, c := range []struct{ name, in, want string }{
		{"relative file", "a/b.txt", filepath.Join(r, "a", "b.txt")},
		{"not-yet-existing file", "a/new/deeper/f.txt", filepath.Join(r, "a", "new", "deeper", "f.txt")},
		{"@ prefix", "@a/b.txt", filepath.Join(r, "a", "b.txt")},
		{"quoted", `"a/b.txt"`, filepath.Join(r, "a", "b.txt")},
		{"inside symlink", "link/b.txt", filepath.Join(r, "a", "b.txt")},
		{"dangling link to inside", "dangling", filepath.Join(r, "inner", "new")},
	} {
		got, err := ws.Resolve(c.in)
		if err != nil {
			t.Errorf("%s: Resolve(%q): %v", c.name, c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: Resolve(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}

	if _, err := ws.Resolve(r + "-evil/x"); !errors.Is(err, ErrPathNotAllowed) {
		t.Errorf("a sibling sharing the root's string prefix: err = %v, want ErrPathNotAllowed", err)
	}
}

// TS-20-9: A path resolving outside the root is refused with the workspace
// path_not_allowed error.
func TestResolveRefusesOutsidePaths_TS20_9(t *testing.T) {
	ws := openDir(t)
	r := ws.Root()
	outside := t.TempDir()
	mustSymlink(t, outside, filepath.Join(r, "out"))

	swapped := filepath.Join(r, "d")
	if err := os.Mkdir(swapped, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(swapped, "f")
	if err := ws.CheckWriteTarget(target); err != nil {
		t.Fatalf("an absent file under a plain in-root directory is fine: %v", err)
	}
	if err := os.Remove(swapped); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, outside, swapped)

	_, errLink := ws.Resolve("out/f")
	_, errAbs := ws.Resolve(filepath.Join(outside, "f"))
	errSwap := ws.CheckWriteTarget(target)

	for name, err := range map[string]error{"through a link": errLink, "absolute": errAbs, "swapped component": errSwap} {
		if !errors.Is(err, ErrPathNotAllowed) {
			t.Errorf("%s: err = %v, want ErrPathNotAllowed", name, err)
			continue
		}
		msg := err.Error()
		if !strings.HasPrefix(msg, "workspace: path_not_allowed") {
			t.Errorf("%s: %q does not start with the workspace sentinel text", name, msg)
		}
		if !strings.Contains(msg, r) {
			t.Errorf("%s: %q does not name the root %s", name, msg, r)
		}
		if !strings.HasSuffix(msg, nextStep) {
			t.Errorf("%s: %q does not end with the next-step sentence", name, msg)
		}
	}
}

// TS-20-10: Malformed paths are refused with the workspace path_malformed error.
func TestMalformedPathsAreRefused_TS20_10(t *testing.T) {
	ws := openDir(t)
	for i := 0; i <= 40; i++ {
		target := fmt.Sprintf("hop%d", i+1)
		if i == 40 {
			target = "missing"
		}
		mustSymlink(t, target, filepath.Join(ws.Root(), fmt.Sprintf("hop%d", i)))
	}

	for _, p := range []string{"", "a\x00b", "file://host/x", "~no-such-user-xyz/x"} {
		if _, err := Normalize(p); !errors.Is(err, ErrPathMalformed) {
			t.Errorf("Normalize(%q) err = %v, want ErrPathMalformed", p, err)
		}
	}
	for _, p := range []string{"", "a\x00b", "file://host/x", "~no-such-user-xyz/x", "hop0"} {
		_, err := ws.Resolve(p)
		if !errors.Is(err, ErrPathMalformed) {
			t.Errorf("Resolve(%q) err = %v, want ErrPathMalformed", p, err)
			continue
		}
		if !strings.HasPrefix(err.Error(), "workspace: path_malformed") {
			t.Errorf("Resolve(%q): %q does not start with the workspace sentinel text", p, err)
		}
	}
}

// TS-20-11: A ~ path with an unresolvable HOME is refused with ErrNoHome.
func TestUnresolvableHomeIsRefused_TS20_11(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	t.Setenv("home", "")
	for _, p := range []string{"~", "~/x"} {
		got, err := Normalize(p)
		if !errors.Is(err, ErrNoHome) {
			t.Errorf("Normalize(%q) err = %v, want ErrNoHome", p, err)
			continue
		}
		if !strings.HasPrefix(err.Error(), "workspace: home directory could not be resolved") {
			t.Errorf("Normalize(%q): %q does not start with the workspace sentinel text", p, err)
		}
		if got != "" {
			t.Errorf("Normalize(%q) returned %q alongside the error", p, got)
		}
	}
}

// TS-20-12: Normalize expands home forms and strips @ and quotes exactly as
// the tools version did.
func TestNormalizeRewrites_TS20_12(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

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
		got, err := Normalize(c.in)
		if err != nil {
			t.Errorf("Normalize(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("Normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TS-20-13: Rel returns a root-relative path under the root and the input
// unchanged otherwise.
func TestRelIsRootRelativeUnderTheRoot_TS20_13(t *testing.T) {
	ws := openDir(t)
	r := ws.Root()
	for _, c := range []struct{ in, want string }{
		{filepath.Join(r, "a", "b"), filepath.Join("a", "b")},
		{r, "."},
		{"/elsewhere", "/elsewhere"},
		{r + "-evil/x", r + "-evil/x"},
	} {
		if got := ws.Rel(c.in); got != c.want {
			t.Errorf("Rel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The tests below moved here from package tools with the code they cover and
// assert the same outcomes.

func TestNormalizationRunsBeforeCanonicalization(t *testing.T) {
	ws := openDir(t)
	// A file:// URL must be unwrapped BEFORE the path is canonicalized. A
	// guard that canonicalizes first sees the literal string "file:/..." as a
	// relative path and joins it to the root, checking a string it will never
	// open.
	target := filepath.Join(ws.Root(), "x.txt")
	if err := os.WriteFile(target, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ws.Resolve("file://" + target)
	if err != nil {
		t.Fatalf("file:// URL was not unwrapped before canonicalization: %v", err)
	}
	if got != target {
		t.Fatalf("resolved to %q, want %q", got, target)
	}
}

func TestPathContainmentRejectsEscapes(t *testing.T) {
	ws := openDir(t)
	for _, p := range []string{"../outside.txt", "a/../../outside.txt", "/etc/passwd"} {
		if _, err := ws.Resolve(p); !errors.Is(err, ErrPathNotAllowed) {
			t.Errorf("Resolve(%q) err = %v, want ErrPathNotAllowed", p, err)
		}
	}
}

// TestContainmentComparesSegmentsNotStringPrefixes: a prefix test says
// /work/space is inside /work/s.
func TestContainmentComparesSegmentsNotStringPrefixes(t *testing.T) {
	base := t.TempDir()
	inside := filepath.Join(base, "s")
	sibling := filepath.Join(base, "space")
	for _, d := range []string{inside, sibling} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := Open(inside, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.Resolve(filepath.Join(sibling, "f.txt")); !errors.Is(err, ErrPathNotAllowed) {
		t.Fatalf("a sibling directory sharing a string prefix was accepted: err = %v", err)
	}
}

func TestNewFileUnderTheWorkspaceIsAllowed(t *testing.T) {
	ws := openDir(t)
	// The file does not exist yet. A guard that EvalSymlinks the whole path
	// fails here, which would make every file the agent creates unreachable.
	if _, err := ws.Resolve("new/nested/file.txt"); err != nil {
		t.Fatalf("a not-yet-created file inside the workspace must resolve: %v", err)
	}
}

func TestTildeUserExpandsToThatUsersHome(t *testing.T) {
	me, err := user.Current()
	if err != nil || me.HomeDir == "" {
		t.Skip("no current user to look up")
	}
	got, err := Normalize("~" + me.Username + "/secrets")
	if err != nil {
		t.Fatalf("Normalize(~%s/secrets): %v", me.Username, err)
	}
	if want := filepath.Join(me.HomeDir, "secrets"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestTildeUnknownUserIsMalformed(t *testing.T) {
	_, err := Normalize("~no-such-user-agentkit/secrets")
	if !errors.Is(err, ErrPathMalformed) {
		t.Fatalf("err = %v, want ErrPathMalformed", err)
	}
}

func TestPathNotAllowedNamesTheRootAndSaysWhatToDoNext(t *testing.T) {
	ws := openDir(t)
	_, err := ws.Resolve("../..")
	if err == nil {
		t.Fatal("Resolve of a path outside the workspace succeeded")
	}
	if !strings.Contains(err.Error(), ws.Root()) {
		t.Errorf("error %q does not name the workspace root %s", err, ws.Root())
	}
	if !strings.Contains(err.Error(), "inside the workspace") {
		t.Errorf("error %q does not say to use a path inside the workspace", err)
	}
}

// TestDanglingSymlinkCannotEscapeTheWorkspace is REQ-SEC-01 / NFR-SEC-02.
//
// EvalSymlinks fails on a dangling link exactly as it fails on a missing
// file, so a guard that resolves the parent and rejoins the base sees
// `ws/link` — inside — while os.WriteFile follows the link and creates the
// file OUTSIDE. The link's target is where containment must be decided.
func TestDanglingSymlinkCannotEscapeTheWorkspace(t *testing.T) {
	outside := t.TempDir()
	ws := openDir(t)
	dir := ws.Root()

	t.Run("dangling link to outside is rejected", func(t *testing.T) {
		mustSymlink(t, filepath.Join(outside, "newfile"), filepath.Join(dir, "escape"))
		if _, err := ws.Resolve("escape"); !errors.Is(err, ErrPathNotAllowed) {
			t.Fatalf("Resolve(escape) err = %v, want ErrPathNotAllowed", err)
		}
	})
	t.Run("dangling link inside is allowed", func(t *testing.T) {
		mustSymlink(t, "inner/newfile", filepath.Join(dir, "stay"))
		if err := os.MkdirAll(filepath.Join(dir, "inner"), 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := ws.Resolve("stay")
		if err != nil {
			t.Fatalf("a dangling link whose target is inside must resolve: %v", err)
		}
		if got != filepath.Join(ws.Root(), "inner", "newfile") {
			t.Fatalf("resolved to %q, want the link TARGET", got)
		}
	})
	t.Run("chained dangling links are followed to the end", func(t *testing.T) {
		mustSymlink(t, filepath.Join(outside, "deep"), filepath.Join(dir, "hop2"))
		mustSymlink(t, "hop2", filepath.Join(dir, "hop1"))
		if _, err := ws.Resolve("hop1"); !errors.Is(err, ErrPathNotAllowed) {
			t.Fatalf("Resolve(hop1) err = %v, want ErrPathNotAllowed: hop1 -> hop2 -> outside", err)
		}
	})
	t.Run("a link loop is malformed, not accepted", func(t *testing.T) {
		mustSymlink(t, "cycle", filepath.Join(dir, "cycle"))
		if _, err := ws.Resolve("cycle"); err == nil {
			t.Fatal("a symlink cycle must be rejected")
		}
	})
}

// TestCheckWriteTargetRefusesALinkPlantedAfterResolve pins the second half
// of the fix: the final path is Lstat'ed immediately before the open.
func TestCheckWriteTargetRefusesALinkPlantedAfterResolve(t *testing.T) {
	outside := t.TempDir()
	ws := openDir(t)
	abs := filepath.Join(ws.Root(), "target.txt")
	if err := ws.CheckWriteTarget(abs); err != nil {
		t.Fatalf("an absent target is fine: %v", err)
	}
	mustSymlink(t, filepath.Join(outside, "x"), abs) // planted between Resolve and open
	if err := ws.CheckWriteTarget(abs); !errors.Is(err, ErrPathNotAllowed) {
		t.Fatalf("err = %v, want ErrPathNotAllowed for a link whose target is outside", err)
	}
}

// TestCheckWriteTargetReResolvesTheWholePath. The leaf is not where the swap
// happens: replace a PARENT directory with a link to outside and the leaf
// looks like an ordinary (or absent) file while the write lands elsewhere.
func TestCheckWriteTargetReResolvesTheWholePath(t *testing.T) {
	outside := t.TempDir()
	ws := openDir(t)
	sub := filepath.Join(ws.Root(), "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(sub, "file.txt")
	if err := os.WriteFile(abs, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ws.CheckWriteTarget(abs); err != nil {
		t.Fatalf("a plain file under a plain directory is fine: %v", err)
	}
	// The swap: sub becomes a link to outside, and outside holds a plain
	// file.txt so the leaf Lstat's as a regular file.
	if err := os.RemoveAll(sub); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "file.txt"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, outside, sub)
	if err := ws.CheckWriteTarget(abs); !errors.Is(err, ErrPathNotAllowed) {
		t.Fatalf("err = %v, want ErrPathNotAllowed: the parent is now a link to outside, "+
			"and a check that looks only at the leaf does not see it", err)
	}
}
