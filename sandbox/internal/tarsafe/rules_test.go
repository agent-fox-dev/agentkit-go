package tarsafe

import (
	"archive/tar"
	"errors"
	"strings"
	"testing"
)

func TestCheckAllowsFilesDirectoriesAndInsideSymlinks(t *testing.T) {
	v := NewValidator(nil)
	for _, e := range []entry{
		folder("d/"), folder("./"), reg("d/f", "x", 0o644), reg("plain", "", 0o600),
		symlink("d/up", "../plain"), symlink("ld", "d"), reg("ld/through", "", 0o644),
		symlink("d/self", "."), folder("ld/sub/"),
	} {
		if _, err := v.Check(&e.hdr); err != nil {
			t.Errorf("Check(%q) = %v, want it allowed", e.hdr.Name, err)
		}
	}
}

func TestCheckRefusesAnEntryNamingWhy(t *testing.T) {
	for _, c := range []struct {
		hdr    tar.Header
		reason string
	}{
		{tar.Header{Typeflag: tar.TypeReg, Name: ""}, "empty name"},
		{tar.Header{Typeflag: tar.TypeReg, Name: "/abs/x"}, "absolute name"},
		{tar.Header{Typeflag: tar.TypeReg, Name: `\abs`}, "absolute name"},
		{tar.Header{Typeflag: tar.TypeReg, Name: "a/../../x"}, ".. component"},
		{tar.Header{Typeflag: tar.TypeReg, Name: "../x"}, ".. component"},
		{tar.Header{Typeflag: tar.TypeLink, Name: "h", Linkname: "x"}, "hard link"},
		{tar.Header{Typeflag: tar.TypeChar, Name: "c"}, "character device"},
		{tar.Header{Typeflag: tar.TypeBlock, Name: "b"}, "block device"},
		{tar.Header{Typeflag: tar.TypeFifo, Name: "p"}, "FIFO"},
		{tar.Header{Typeflag: 'Z', Name: "z"}, "not allowed"},
		{tar.Header{Typeflag: tar.TypeReg, Name: "./"}, "target directory itself"},
		{tar.Header{Typeflag: tar.TypeSymlink, Name: "l"}, "empty symlink target"},
		{tar.Header{Typeflag: tar.TypeSymlink, Name: "l", Linkname: "/etc"}, "is absolute"},
		{tar.Header{Typeflag: tar.TypeSymlink, Name: "d/l", Linkname: "../../x"}, "leaves the target"},
	} {
		_, err := NewValidator(nil).Check(&c.hdr)
		var ee *EntryError
		if !errors.As(err, &ee) || ee.Name != c.hdr.Name || !strings.Contains(err.Error(), c.reason) {
			t.Errorf("Check(%q, type %q) = %v, want an EntryError naming it with %q", c.hdr.Name, c.hdr.Typeflag, err, c.reason)
		}
	}
}

// A symlink the stream wrote earlier, or one already on disk, cannot carry a
// later entry, or another link's target, outside the target directory.
func TestCheckRefusesAPathThatResolvesThroughASymlinkToOutside(t *testing.T) {
	disk := func(name string) (string, bool, error) {
		switch name {
		case "abs":
			return "/etc", true, nil
		case "up":
			return "..", true, nil
		case "loop":
			return "loop", true, nil
		case "broken":
			return "", false, errors.New("cannot inspect")
		}
		return "", false, nil
	}
	for _, c := range []struct {
		earlier []entry
		e       entry
	}{
		{nil, reg("abs/x", "", 0o644)},
		{nil, reg("up/x", "", 0o644)},
		{nil, folder("abs/")},
		{nil, reg("loop/x", "", 0o644)},
		{nil, reg("broken/x", "", 0o644)},
		{nil, symlink("t", "up/x")},
		{[]entry{folder("q/"), symlink("q/p", "..")}, symlink("q/p2", "p/..")},
	} {
		v := NewValidator(disk)
		for _, e := range c.earlier {
			if _, err := v.Check(&e.hdr); err != nil {
				t.Fatalf("earlier %q refused: %v", e.hdr.Name, err)
			}
		}
		if _, err := v.Check(&c.e.hdr); err == nil || !strings.Contains(err.Error(), "outside the target") && !strings.Contains(err.Error(), "leaves the target") {
			t.Errorf("after %v, Check(%q) = %v, want it refused", c.earlier, c.e.hdr.Name, err)
		}
	}
}

// A non-link entry the stream wrote shadows a link on disk under its name.
func TestCheckTrustsTheStreamsOwnEntryOverTheDisk(t *testing.T) {
	v := NewValidator(func(string) (string, bool, error) { return "/etc", true, nil })
	f := reg("x", "", 0o644)
	if _, err := v.Check(&f.hdr); err != nil {
		t.Fatal(err)
	}
	link := symlink("l", "x")
	if _, err := v.Check(&link.hdr); err != nil {
		t.Errorf("a link to the stream's own file x was refused: %v", err)
	}
}

func TestCheckClearsSpecialBitsAndNormalisesTheType(t *testing.T) {
	in := &tar.Header{Typeflag: 0, Name: "f", Mode: 0o6777 | 0o1000}
	out, err := NewValidator(nil).Check(in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Mode != 0o777 || out.Typeflag != tar.TypeReg {
		t.Errorf("Check = mode %o type %q, want 0777 and a regular file", out.Mode, out.Typeflag)
	}
	if in.Mode != 0o7777 {
		t.Error("Check changed the caller's header")
	}
}

func TestSymlinkEscapes(t *testing.T) {
	for _, c := range []struct {
		name, target string
		want         bool
	}{
		{"l", "a", false}, {"a/l", "..", false}, {"a/b/l", "../../c", false}, {"l", ".", false},
		{"l", "..", true}, {"a/l", "../..", true}, {"l", "/etc", true}, {"l", "a/../../b", true},
	} {
		if got := SymlinkEscapes(c.name, c.target); got != c.want {
			t.Errorf("SymlinkEscapes(%q, %q) = %v, want %v", c.name, c.target, got, c.want)
		}
	}
}

func TestEntryErrorNamesTheEntryOrTheStream(t *testing.T) {
	if got := (&EntryError{Name: "x", Reason: "r"}).Error(); got != `unsafe tar entry "x": r` {
		t.Errorf("Error() = %q", got)
	}
	if got := (&EntryError{Reason: "r"}).Error(); got != "unsafe tar stream: r" {
		t.Errorf("Error() = %q", got)
	}
}
