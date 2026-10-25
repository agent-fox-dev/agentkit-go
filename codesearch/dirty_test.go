//go:build !windows

package codesearch

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// filler is n small Go files, enough that a few dirty paths stay under the
// rebuild threshold.
func filler(n int) map[string]string {
	return numbered(n, "f%03d.go", "package pkg\n// token%03d\nfunc F%03d() {}\n")
}

// setMtimes sets the modification time of every given file.
func setMtimes(t *testing.T, idx *Index, when time.Time, rels ...string) {
	t.Helper()
	for _, rel := range rels {
		if err := os.Chtimes(filepath.Join(idx.ws.Root(), rel), when, when); err != nil {
			t.Fatal(err)
		}
	}
}

// sameSizeEdit replaces rel's content with content of the same size and puts
// its mtime back, as a filesystem's timestamp granularity can.
func sameSizeEdit(t *testing.T, idx *Index, rel, content string) {
	t.Helper()
	fi, err := os.Stat(filepath.Join(idx.ws.Root(), rel))
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(content)) != fi.Size() {
		t.Fatalf("fixture: %d bytes for a %d-byte file", len(content), fi.Size())
	}
	write(t, idx, rel, content)
	setMtimes(t, idx, fi.ModTime(), rel)
}

func TestInvalidateNeverBlocksNorPanics(t *testing.T) {
	idx := newFixture(t, filler(5), Options{})
	started, release := blockOutline(t, idx)
	first := async(context.Background(), idx, "token000")
	recv(t, started, "the build")

	marked := make(chan struct{})
	go func() {
		idx.Invalidate("f000.go")
		idx.Invalidate("")
		close(marked)
	}()
	recv(t, marked, "Invalidate during a build")
	if !stays(first) {
		t.Fatal("the build finished early")
	}
	release()
	recv(t, first, "the first call")

	if err := idx.Close(); err != nil {
		t.Fatal(err)
	}
	idx.Invalidate("x.go")
	idx.Invalidate("")
	var none *Index
	none.Invalidate("x.go")
}

// Edits, new files and deletions reported through Invalidate reach the next
// search through the overlay.
func TestCodeSearchSeesTheChangesTheToolsReport(t *testing.T) {
	idx := newFixture(t, with(filler(100), map[string]string{
		"a.go": "package pkg\n// oldToken\nfunc A() {}\n",
		"b.go": "package pkg\n// keepToken\nfunc B() {}\n",
	}), Options{})
	find(t, idx, "oldToken")

	write(t, idx, "a.go", "package pkg\n// newToken\nfunc A() {}\n")
	idx.Invalidate("a.go")
	r := find(t, idx, "newToken")
	if !slices.Equal(paths(r), []string{"a.go"}) || r.Data["dirty_files"] != 1 {
		t.Errorf("after an edit: newToken in %v, dirty_files %v, want [a.go] and 1", paths(r), r.Data["dirty_files"])
	}
	if got := paths(find(t, idx, "oldToken")); len(got) != 0 {
		t.Errorf("after an edit: oldToken still found in %v", got)
	}

	write(t, idx, "new.go", "package pkg\n// freshWord\nfunc New() {}\n")
	idx.Invalidate("new.go")
	if got := paths(find(t, idx, "freshWord")); !slices.Equal(got, []string{"new.go"}) {
		t.Errorf("a new file: freshWord in %v, want [new.go]", got)
	}

	remove(t, idx, "b.go")
	idx.Invalidate("b.go")
	if got := paths(find(t, idx, "keepToken")); len(got) != 0 {
		t.Errorf("a deleted file is still found: %v", got)
	}
	if n := idx.BuildCount(); n != 1 {
		t.Errorf("BuildCount = %d, want 1: the overlay serves a few dirty files", n)
	}
}

// Invalidate("") is what a shell command reports: the walk that follows finds
// a same-size edit, a deletion and a new file.
func TestRevalidationFindsChangesMadeBehindTheToolsBack(t *testing.T) {
	idx := newFixture(t, with(filler(100), map[string]string{
		"f.txt":    "aaaa content here\n",
		"gone.txt": "goneToken content\n",
	}), Options{})
	find(t, idx, "aaaa")

	write(t, idx, "f.txt", "bbbb content here\n")
	remove(t, idx, "gone.txt")
	write(t, idx, "n.txt", "newFileToken\n")
	idx.Invalidate("")

	for q, want := range map[string][]string{"bbbb": {"f.txt"}, "aaaa": nil, "goneToken": nil, "newFileToken": {"n.txt"}} {
		if got := paths(find(t, idx, q)); !slices.Equal(got, want) {
			t.Errorf("%s found in %v, want %v", q, got, want)
		}
	}
	if n := idx.RevalCount(); n != 1 {
		t.Errorf("RevalCount = %d, want 1", n)
	}
}

// A mark walks the workspace again only when it can change what the walk
// yields: a path the index does not hold, or a .gitignore, each edit of it.
func TestRevalidationFollowsTheMarks(t *testing.T) {
	idx := newFixture(t, with(filler(100), map[string]string{
		"b.go": "package pkg\n// bToken\nfunc B() {}\n",
		"c.go": "package pkg\n// cToken\nfunc C() {}\n",
	}), Options{})
	find(t, idx, "bToken")
	backdate(idx)

	gitignore := func(content string) func() {
		return func() {
			write(t, idx, ".gitignore", content)
			idx.Invalidate(".gitignore")
		}
	}
	for _, s := range []struct {
		name  string
		act   func()
		query string
		walks int
		want  []string
	}{
		{"an indexed file edited", func() {
			write(t, idx, "b.go", "package pkg\n// bEdited\nfunc B() {}\n")
			idx.Invalidate("b.go")
		}, "bEdited", 0, []string{"b.go"}},
		{"a new file", func() {
			write(t, idx, "n.go", "package pkg\n// nToken\nfunc N() {}\n")
			idx.Invalidate("n.go")
		}, "nToken", 1, []string{"n.go"}},
		{"nothing new", func() {}, "nToken", 0, []string{"n.go"}},
		{"a .gitignore edit hiding nothing", gitignore("nothing.txt\n"), "cToken", 1, []string{"c.go"}},
		{"a second edit hiding a file", gitignore("c.go\n"), "cToken", 1, nil},
		{"a third showing it again", gitignore("nothing.txt\n"), "cToken", 1, []string{"c.go"}},
		{"an edit hiding the new file", gitignore("n.go\n"), "nToken", 1, nil},
	} {
		before := idx.RevalCount()
		s.act()
		got := paths(find(t, idx, s.query))
		if walks := idx.RevalCount() - before; walks != s.walks || !slices.Equal(got, s.want) {
			t.Errorf("%s: %d walks, %s in %v; want %d walks and %v", s.name, walks, s.query, got, s.walks, s.want)
		}
	}
	if n := idx.BuildCount(); n != 1 {
		t.Errorf("BuildCount = %d: the changes were repaired by a rebuild, not by revalidation", n)
	}
}

func TestNoInvalidateMeansNoRevalidationAndNoRebuild(t *testing.T) {
	idx := newFixture(t, filler(3), Options{})
	for range 5 {
		find(t, idx, "token001")
	}
	if idx.BuildCount() != 1 || idx.RevalCount() != 0 {
		t.Errorf("BuildCount = %d, RevalCount = %d, want 1 and 0", idx.BuildCount(), idx.RevalCount())
	}
}

func TestAMarkMadeDuringRevalidationSurvives(t *testing.T) {
	idx := newFixture(t, with(filler(100), map[string]string{"a.go": "package pkg\n// original\nfunc A() {}\n"}), Options{})
	find(t, idx, "original")

	entered, gate := make(chan struct{}), make(chan struct{})
	idx.testRevalHook = func() {
		close(entered)
		<-gate
	}
	idx.Invalidate("")
	query := async(context.Background(), idx, "token001")
	recv(t, entered, "the revalidation walk")
	write(t, idx, "a.go", "package pkg\n// midWalk\nfunc A() {}\n")
	idx.Invalidate("a.go")
	close(gate)
	recv(t, query, "the query")
	idx.testRevalHook = nil

	if got := paths(find(t, idx, "midWalk")); !slices.Equal(got, []string{"a.go"}) {
		t.Errorf("midWalk found in %v, want [a.go]: the mark made during the walk was lost", got)
	}
}

// A call whose context ends during the walk is aborted; the walk does not
// count and leaves the whole-index mark, so the next call walks in full.
func TestAnAbandonedRevalidationIsWalkedAgain(t *testing.T) {
	idx := newFixture(t, filler(20), Options{})
	find(t, idx, "token000")
	backdate(idx)
	remove(t, idx, "f005.go")
	idx.Invalidate("")

	ctx, cancel := context.WithCancel(context.Background())
	idx.testRevalHook = cancel
	r := call(ctx, idx, map[string]any{"query": "token000"})
	idx.testRevalHook = nil
	if r.Error != "aborted" || idx.RevalCount() != 0 {
		t.Fatalf("call = %v %q, RevalCount = %d; want aborted and no walk counted", r.OK, r.Error, idx.RevalCount())
	}

	if got := paths(find(t, idx, "token000")); !slices.Equal(got, []string{"f000.go"}) {
		t.Errorf("after the abandoned walk token000 is in %v, want [f000.go]", got)
	}
	if got := paths(find(t, idx, "token005")); len(got) != 0 || idx.RevalCount() != 1 {
		t.Errorf("token005 in %v, RevalCount %d; want the deletion seen by one full walk", got, idx.RevalCount())
	}
}

// A file whose size and mtime match its record, written well before the
// build, is trusted without being read: an edit that kept both is not seen.
func TestRevalidationTrustsTheSizeAndMtimeOfAnOldFile(t *testing.T) {
	files := filler(20)
	idx := newFixture(t, files, Options{})
	rels := make([]string, 0, len(files))
	for rel := range files {
		rels = append(rels, rel)
	}
	setMtimes(t, idx, time.Now().Add(-time.Hour), rels...)
	find(t, idx, "token000")

	sameSizeEdit(t, idx, "f007.go", "package pkg\n// toXXX007\nfunc F007() {}\n")
	idx.Invalidate("")
	r := find(t, idx, "token007")
	if !slices.Equal(paths(r), []string{"f007.go"}) || r.Data["dirty_files"] != 0 {
		t.Errorf("token007 in %v, dirty_files %v: the walk read a file it could trust", paths(r), r.Data["dirty_files"])
	}
}

// Files written just before the build are racy: their content is compared
// with what was indexed, so an unchanged tree is not marked dirty and a
// same-size edit that kept the mtime is still caught.
func TestRevalidationInsideTheRacyWindowMarksOnlyRealEdits(t *testing.T) {
	idx := newFixture(t, filler(100), Options{})
	find(t, idx, "token000")

	idx.Invalidate("")
	if r := find(t, idx, "token001"); r.Data["dirty_files"] != 0 {
		t.Errorf("an unchanged tree: dirty_files = %v, want 0", r.Data["dirty_files"])
	}

	sameSizeEdit(t, idx, "f010.go", "package pkg\n// toXXX010\nfunc F010() {}\n")
	idx.Invalidate("")
	r := find(t, idx, "toXXX010")
	if !slices.Equal(paths(r), []string{"f010.go"}) || r.Data["dirty_files"] != 1 {
		t.Errorf("toXXX010 in %v, dirty_files %v, want [f010.go] and 1", paths(r), r.Data["dirty_files"])
	}
	if n := idx.BuildCount(); n != 1 {
		t.Errorf("BuildCount = %d, want 1", n)
	}
}

// A racy file compared once after its timestamp granule closed cannot hide a
// same-mtime edit any more, so later walks trust it without reading it.
func TestAVerifiedRacyFileIsNotReadAgain(t *testing.T) {
	files := filler(20)
	idx := newFixture(t, files, Options{})
	mtime := time.Now().Add(-3 * time.Second)
	rels := make([]string, 0, len(files))
	for rel := range files {
		rels = append(rels, rel)
	}
	setMtimes(t, idx, mtime, rels...)
	find(t, idx, "token000")
	// Recorded as indexed a second after the write: racy, its granule closed.
	idx.mu.Lock()
	for rel, fi := range idx.fileInfos {
		fi.indexedAt = mtime.Add(time.Second)
		idx.fileInfos[rel] = fi
	}
	idx.mu.Unlock()

	idx.Invalidate("") // reads the racy files, finds them unchanged
	find(t, idx, "token007")
	sameSizeEdit(t, idx, "f007.go", "package pkg\n// toXXX007\nfunc F007() {}\n")
	idx.Invalidate("")
	if got := paths(find(t, idx, "token007")); !slices.Equal(got, []string{"f007.go"}) {
		t.Errorf("token007 in %v: the second walk read a verified file again", got)
	}
	if n := idx.RevalCount(); n != 2 {
		t.Errorf("RevalCount = %d, want 2", n)
	}
}

// After any sequence of writes, edits, shell edits, deletions and creations,
// a search shows the last content and none that was overwritten or deleted.
func TestCodeSearchNeverShowsContentOlderThanTheLastChange(t *testing.T) {
	rng := rand.New(rand.NewPCG(42, 49))
	for trial := range 4 {
		t.Run(fmt.Sprint(trial), func(t *testing.T) {
			model := numbered(5, "f%d.go", "package pkg\n// tok%d\n")
			idx := newFixture(t, model, Options{})
			find(t, idx, "tok0")
			pick := func() string {
				rels := make([]string, 0, len(model))
				for rel := range model {
					rels = append(rels, rel)
				}
				sort.Strings(rels)
				return rels[rng.IntN(len(rels))]
			}

			var last string
			for op := range 1 + rng.IntN(15) {
				last = fmt.Sprintf("tok%d", 100+op)
				content := "package pkg\n// " + last + "\n"
				switch kind := rng.IntN(4); {
				case kind == 0: // write_file, maybe a new file
					rel := fmt.Sprintf("f%d.go", rng.IntN(8))
					write(t, idx, rel, content)
					model[rel] = content
					idx.Invalidate(rel)
				case kind == 1 && len(model) > 1: // a shell delete, then a create
					rel := pick()
					remove(t, idx, rel)
					delete(model, rel)
					fallthrough
				case kind == 2: // a shell create
					rel := fmt.Sprintf("new%d.go", op)
					write(t, idx, rel, content)
					model[rel] = content
					idx.Invalidate("")
				default: // edit_file or a shell edit
					rel := pick()
					write(t, idx, rel, content)
					model[rel] = content
					if rng.IntN(2) == 0 {
						idx.Invalidate(rel)
					} else {
						idx.Invalidate("")
					}
				}
			}

			r := find(t, idx, "tok", "max_files", 25)
			want := []string{}
			for rel, content := range model {
				if strings.Contains(content, "tok") {
					want = append(want, rel)
				}
			}
			got := paths(r)
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("tok found in %v, want %v", got, want)
			}
			if !strings.Contains(r.Text, last) {
				t.Errorf("the last token %s is not shown", last)
			}
			for _, line := range strings.Split(r.Text, "\n") {
				_, text, ok := strings.Cut(line, ": // ")
				if ok && !containsValue(model, "// "+text+"\n") {
					t.Errorf("stale content shown: %q", line)
				}
			}
		})
	}
}

func containsValue(m map[string]string, sub string) bool {
	for _, v := range m {
		if strings.Contains(v, sub) {
			return true
		}
	}
	return false
}

// A file a revalidation walk found gone, and a later walk finds again with
// changed content, is searchable again.
func TestAFileTheWalkFoundGoneIsFoundAgainWhenItComesBack(t *testing.T) {
	for _, c := range []struct {
		name       string
		hide, show func(idx *Index)
	}{
		{"deleted then recreated by a shell",
			func(idx *Index) { remove(t, idx, "b.go"); idx.Invalidate("") },
			func(idx *Index) {
				write(t, idx, "b.go", "package pkg\n// bAgain, recreated\nfunc B() {}\n")
				idx.Invalidate("")
			}},
		{"edited, then ignored and un-ignored",
			func(idx *Index) {
				write(t, idx, "b.go", "package pkg\n// bAgain, edited\nfunc B() {}\n")
				idx.Invalidate("b.go")
				write(t, idx, ".gitignore", "b.go\n")
				idx.Invalidate(".gitignore")
			},
			func(idx *Index) { write(t, idx, ".gitignore", "nothing.txt\n"); idx.Invalidate(".gitignore") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			idx := newFixture(t, with(filler(100), map[string]string{"b.go": "package pkg\n// bToken\nfunc B() {}\n"}), Options{})
			find(t, idx, "bToken")
			backdate(idx)
			c.hide(idx)
			if got := paths(find(t, idx, "bToken bAgain")); len(got) != 0 {
				t.Fatalf("hidden b.go still found: %v", got)
			}
			c.show(idx)
			if got := paths(find(t, idx, "bAgain")); !slices.Equal(got, []string{"b.go"}) {
				t.Errorf("bAgain found in %v, want [b.go]", got)
			}
		})
	}
}
