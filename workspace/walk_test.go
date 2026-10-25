package workspace

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
)

// The walk visits files and directories with slash paths relative to sub,
// and its ignore rules are rooted at sub: the global layer applies, the root's
// .gitignore and repository exclude file do not.
func TestWalkVisitsTheKeptEntriesRelativeToSub(t *testing.T) {
	root := testutil.Tree(t, map[string]string{
		".git/info/exclude": "*.excl\n", ".gitignore": "*.gen\nignored/\n",
		"a.txt": "", "a.gen": "", "b.glob": "", "b.excl": "", "ignored/f": "",
		"sub/k.txt": "", "sub/x.gen": "", "sub/y.glob": "", "sub/z.excl": "",
		"sub/.gitignore": "*.k\n", "sub/n.k": "", "other/deep/f.txt": "",
	})
	ws, err := Open(root, Options{Ignore: withGlobalExcludes(t, "*.glob\n")})
	if err != nil {
		t.Fatal(err)
	}
	wantRoot := []string{"a.txt", "other", "other/deep", "other/deep/f.txt", "sub", "sub/k.txt"}
	wantSub := []string{"k.txt", "x.gen", "z.excl"}
	for sub, want := range map[string][]string{
		"": wantRoot, ".": wantRoot, "sub": wantSub, filepath.Join(ws.Root(), "sub"): wantSub, "a/../sub": wantSub,
	} {
		if got := walked(t, ws, sub, WalkOptions{}); !slices.Equal(got, want) {
			t.Errorf("Walk(%q) = %v, want %v", sub, got, want)
		}
	}
}

func TestWalkRefusesASubOutsideTheRootBeforeCallingFn(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	testutil.WriteTree(t, base, map[string]string{"ws/a/f.txt": "", "ws-evil/g.txt": ""})
	ws := open(t, root)
	fn := func(rel string, _ fs.DirEntry) error { t.Errorf("fn called for %q", rel); return nil }
	for _, sub := range []string{"../x", filepath.Join(base, "x"), ws.Root() + "-evil", "a/../../x"} {
		if err := ws.Walk(context.Background(), sub, WalkOptions{}, fn); !errors.Is(err, ErrPathNotAllowed) {
			t.Errorf("Walk(%q) err = %v, want ErrPathNotAllowed", sub, err)
		}
	}
	var none *Workspace
	if err := none.Walk(context.Background(), "", WalkOptions{}, fn); !errors.Is(err, ErrPathNotAllowed) {
		t.Errorf("Walk on a nil workspace: err = %v, want ErrPathNotAllowed", err)
	}
}

func TestWalkSkipsHiddenEntriesUnlessAsked(t *testing.T) {
	ws, err := Open(testutil.Tree(t, map[string]string{
		"a.txt": "", ".hidden/f": "", ".env": "", "x.secret": "", ".h.secret": "",
	}), Options{Ignore: withGlobalExcludes(t, "*.secret\n")})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := walked(t, ws, "", WalkOptions{}), []string{"a.txt"}; !slices.Equal(got, want) {
		t.Errorf("Walk = %v, want %v", got, want)
	}
	// The rules still apply to hidden entries.
	if got, want := walked(t, ws, "", WalkOptions{IncludeHidden: true}), []string{".env", ".hidden", ".hidden/f", "a.txt"}; !slices.Equal(got, want) {
		t.Errorf("Walk with IncludeHidden = %v, want %v", got, want)
	}
}

// A symlink is visited as an entry and not followed, and that holds for a
// sub that names one.
func TestWalkDoesNotFollowSymlinks(t *testing.T) {
	outside := testutil.Tree(t, map[string]string{"secret.txt": ""})
	ws := openTree(t, map[string]string{"target/inner.txt": "", "real.txt": ""})
	mustSymlink(t, outside, filepath.Join(ws.Root(), "linkout"))
	mustSymlink(t, "target", filepath.Join(ws.Root(), "linkin"))
	mustSymlink(t, "real.txt", filepath.Join(ws.Root(), "file_link.txt"))
	want := []string{"file_link.txt", "linkin", "linkout", "real.txt", "target", "target/inner.txt"}
	if got := walked(t, ws, "", WalkOptions{}); !slices.Equal(got, want) {
		t.Errorf("Walk = %v, want %v", got, want)
	}
	if got := walked(t, ws, "linkout", WalkOptions{}); len(got) != 0 {
		t.Errorf("Walk(linkout) = %v, want nothing", got)
	}
}

func TestWalkSkipsAnUnreadableDirectory(t *testing.T) {
	ws := openTree(t, map[string]string{"a.txt": "", "locked/inside.txt": ""})
	locked := filepath.Join(ws.Root(), "locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if _, err := os.ReadDir(locked); err == nil {
		t.Skip("the directory is still readable (running as root, or modes are not enforced)")
	}
	if got := walked(t, ws, "", WalkOptions{}); slices.Contains(got, "locked/inside.txt") || !slices.Contains(got, "a.txt") {
		t.Errorf("Walk = %v, want a.txt and nothing under locked", got)
	}
}

func TestWalkOfAMissingSubOrAFileVisitsNothing(t *testing.T) {
	ws := openTree(t, map[string]string{"a.txt": "x"})
	for _, sub := range []string{"missing", "a.txt"} {
		if got := walked(t, ws, sub, WalkOptions{}); len(got) != 0 {
			t.Errorf("Walk(%q) = %v, want nothing", sub, got)
		}
	}
}

func TestWalkStopsWithTheContextError(t *testing.T) {
	files := map[string]string{}
	for i := range 20 {
		files[fmt.Sprintf("f%02d.txt", i)] = ""
	}
	ws := openTree(t, files)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	err := ws.Walk(ctx, "", WalkOptions{}, func(string, fs.DirEntry) error {
		if calls++; calls == 3 {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || calls != 3 {
		t.Errorf("cancelled at the 3rd call: err = %v after %d calls", err, calls)
	}
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	calls = 0
	err = ws.Walk(expired, "", WalkOptions{}, func(string, fs.DirEntry) error { calls++; return nil })
	if !errors.Is(err, context.DeadlineExceeded) || calls != 0 {
		t.Errorf("expired deadline: err = %v after %d calls", err, calls)
	}
}

func TestWalkHonoursWhatFnReturns(t *testing.T) {
	ws := openTree(t, map[string]string{"d1/f1": "", "d1/f2": "", "d2/g1": "", "d3/h": ""})
	stop := errors.New("stop here")
	for _, c := range []struct {
		name    string
		at      string
		ret     error
		wantErr error
		want    []string
	}{
		{"SkipDir on a directory", "d1", filepath.SkipDir, nil, []string{"d1", "d2", "d2/g1", "d3", "d3/h"}},
		{"SkipDir on a file", "d1/f1", filepath.SkipDir, nil, []string{"d1", "d1/f1", "d2", "d2/g1", "d3", "d3/h"}},
		{"SkipAll on a file", "d1/f1", filepath.SkipAll, nil, []string{"d1", "d1/f1"}},
		{"SkipAll on a directory", "d2", filepath.SkipAll, nil, []string{"d1", "d1/f1", "d1/f2", "d2"}},
		{"an error on a file", "d1/f2", stop, stop, []string{"d1", "d1/f1", "d1/f2"}},
		{"an error on a directory", "d2", stop, stop, []string{"d1", "d1/f1", "d1/f2", "d2"}},
	} {
		var got []string
		err := ws.Walk(context.Background(), "", WalkOptions{}, func(rel string, _ fs.DirEntry) error {
			got = append(got, rel)
			if rel == c.at {
				return c.ret
			}
			return nil
		})
		if err != c.wantErr || !slices.Equal(got, c.want) {
			t.Errorf("%s: visited %v, err %v; want %v, %v", c.name, got, err, c.want, c.wantErr)
		}
	}
}
