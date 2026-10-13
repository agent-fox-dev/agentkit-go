package rsyncx

import (
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/tarsafe"
)

// Filtered is a read-only view of root in which every path the matcher
// excludes, and every symlink whose target leaves the transfer root, does not
// exist. Symlinks are judged each time they are served, so a link created
// after the rules were computed is hidden too. The matcher's rules are relative to under, the
// transfer root inside root ("." for root itself); paths outside under are
// left as they are, so the library can reach under through its ancestors.
// The sender serves this view, so the rsync library never sees a rule.
func Filtered(root *os.Root, under string, m *Matcher) fs.FS {
	return &filtered{base: root.FS(), under: path.Clean(under), m: m}
}

type filtered struct {
	base  fs.FS
	under string
	m     *Matcher
}

// hidden reports whether name is an excluded path under the transfer root,
// or a symlink there whose target leaves it.
func (f *filtered) hidden(name string) bool {
	name = path.Clean(name)
	rel := name
	if f.under != "." {
		if name == f.under {
			return false
		}
		r, ok := strings.CutPrefix(name, f.under+"/")
		if !ok {
			return false
		}
		rel = r
	}
	if rel == "." {
		return false
	}
	if f.m.Excluded(rel) {
		return true
	}
	fi, err := fs.Lstat(f.base, name)
	if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		return false
	}
	target, err := fs.ReadLink(f.base, name)
	return err != nil || tarsafe.SymlinkEscapes(rel, target)
}

func (f *filtered) notExist(op, name string) error {
	return &fs.PathError{Op: op, Path: name, Err: fs.ErrNotExist}
}

func (f *filtered) Open(name string) (fs.File, error) {
	if f.hidden(name) {
		return nil, f.notExist("open", name)
	}
	return f.base.Open(name)
}

func (f *filtered) ReadDir(name string) ([]fs.DirEntry, error) {
	if f.hidden(name) {
		return nil, f.notExist("readdir", name)
	}
	ents, err := fs.ReadDir(f.base, name)
	out := ents[:0]
	for _, e := range ents {
		if !f.hidden(path.Join(name, e.Name())) {
			out = append(out, e)
		}
	}
	return out, err
}

func (f *filtered) Stat(name string) (fs.FileInfo, error) {
	if f.hidden(name) {
		return nil, f.notExist("stat", name)
	}
	return fs.Stat(f.base, name)
}

func (f *filtered) Lstat(name string) (fs.FileInfo, error) {
	if f.hidden(name) {
		return nil, f.notExist("lstat", name)
	}
	return fs.Lstat(f.base, name)
}

func (f *filtered) ReadLink(name string) (string, error) {
	if f.hidden(name) {
		return "", f.notExist("readlink", name)
	}
	return fs.ReadLink(f.base, name)
}

// PresentSet lists every entry of fsys under the transfer root under, as
// paths relative to it: the sender's transfer set when fsys is filtered.
func PresentSet(fsys fs.FS, under string) ([]string, error) {
	var out []string
	err := fs.WalkDir(fsys, under, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == under {
			return nil
		}
		rel := p
		if under != "." {
			rel = strings.TrimPrefix(p, under+"/")
		}
		out = append(out, rel)
		return nil
	})
	return out, err
}
