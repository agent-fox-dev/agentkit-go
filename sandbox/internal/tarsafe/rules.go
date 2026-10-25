package tarsafe

import (
	"archive/tar"
	"fmt"
	"path"
	"slices"
	"strings"
)

// EntryError is a tar entry the rules refuse, or a malformed archive.
type EntryError struct {
	Name   string
	Reason string
}

func (e *EntryError) Error() string {
	if e.Name == "" {
		return "unsafe tar stream: " + e.Reason
	}
	return fmt.Sprintf("unsafe tar entry %q: %s", e.Name, e.Reason)
}

// specialBits are setuid, setgid and sticky, cleared from every entry.
const specialBits = 0o4000 | 0o2000 | 0o1000

// maxLinkDepth bounds how many symlinks one resolution follows.
const maxLinkDepth = 40

// Lookup reports whether name, relative to the target directory, is an
// existing symlink and what it points to. A name that does not exist is not
// a link and not an error.
type Lookup func(name string) (target string, isLink bool, err error)

// Validator applies the entry rules to one stream, entry by entry. It
// remembers the stream's own symlinks, and consults lookup (when set) for
// symlinks already on disk.
type Validator struct {
	lookup Lookup
	// seen maps an entry written by this stream to its symlink target, or
	// to "" for an entry that replaced whatever was on disk with a non-link.
	seen map[string]string
}

// NewValidator returns a Validator; lookup may be nil.
func NewValidator(lookup Lookup) *Validator {
	return &Validator{lookup: lookup, seen: map[string]string{}}
}

// Check applies the rules to hdr. It allows only regular files, directories
// and symlinks whose target, resolved against the link's own directory,
// stays inside the target directory; it refuses absolute names, names with a
// ".." component, every other entry type, and an entry whose path resolves
// through a symlink to outside. On success it returns a copy of hdr with the
// setuid, setgid and sticky bits cleared.
func (v *Validator) Check(hdr *tar.Header) (*tar.Header, error) {
	refuse := func(format string, args ...any) (*tar.Header, error) {
		return nil, &EntryError{Name: hdr.Name, Reason: fmt.Sprintf(format, args...)}
	}
	name := hdr.Name
	switch {
	case name == "":
		return refuse("empty name")
	case isAbs(name):
		return refuse("absolute name")
	case slices.Contains(strings.Split(name, "/"), ".."):
		return refuse("name has a .. component")
	}
	switch hdr.Typeflag {
	case tar.TypeReg, 0, tar.TypeDir, tar.TypeSymlink:
	case tar.TypeLink:
		return refuse("hard link")
	case tar.TypeChar:
		return refuse("character device")
	case tar.TypeBlock:
		return refuse("block device")
	case tar.TypeFifo:
		return refuse("FIFO")
	default:
		return refuse("entry type %q is not allowed", hdr.Typeflag)
	}
	clean := path.Clean(name)
	if clean == "." && hdr.Typeflag != tar.TypeDir {
		return refuse("names the target directory itself")
	}
	// realDir is the entry's directory with every symlink in it followed: a
	// link's target is relative to where the link really is, which is not its
	// lexical directory when that directory runs through a link.
	var realDir []string
	if dir := path.Dir(clean); dir != "." {
		var ok bool
		if realDir, ok = v.resolve(nil, dir, 0); !ok {
			return refuse("path resolves through a symlink to outside the target")
		}
	}
	target := ""
	switch hdr.Typeflag {
	case tar.TypeDir:
		if _, ok := v.resolve(nil, clean, 0); !ok {
			return refuse("path resolves through a symlink to outside the target")
		}
	case tar.TypeSymlink:
		target = hdr.Linkname
		switch {
		case target == "":
			return refuse("empty symlink target")
		case isAbs(target):
			return refuse("symlink target %q is absolute", target)
		case SymlinkEscapes(clean, target):
			return refuse("symlink target %q leaves the target directory", target)
		}
		if _, ok := v.resolve(realDir, target, 0); !ok {
			return refuse("symlink target %q leaves the target directory", target)
		}
	}
	if hdr.Typeflag != tar.TypeDir {
		v.seen[clean] = target
	}
	out := *hdr
	out.Mode &^= specialBits
	if out.Typeflag == 0 {
		out.Typeflag = tar.TypeReg
	}
	return &out, nil
}

// SymlinkEscapes reports whether a symlink named name (slash-separated,
// relative to a root) with the given target leaves the root: the target is
// absolute, or, resolved lexically against the link's own directory, it
// climbs above the root. Tar and rsync transfers share this predicate.
func SymlinkEscapes(name, target string) bool {
	if isAbs(target) {
		return true
	}
	p := path.Join(path.Dir(path.Clean(name)), target)
	return p == ".." || strings.HasPrefix(p, "../")
}

// resolve walks rel from the directory base (components relative to the
// target, "." meaning the target), following every symlink it knows of, and
// returns where it ends. ok is false when the walk leaves the target, meets
// an absolute link, follows too many links, or cannot inspect a path.
func (v *Validator) resolve(base []string, rel string, depth int) (_ []string, ok bool) {
	if depth > maxLinkDepth {
		return nil, false
	}
	cur := slices.DeleteFunc(slices.Clone(base), func(c string) bool { return c == "." || c == "" })
	for _, c := range strings.Split(rel, "/") {
		switch c {
		case "", ".":
			continue
		case "..":
			if len(cur) == 0 {
				return nil, false
			}
			cur = cur[:len(cur)-1]
			continue
		}
		cur = append(cur, c)
		target, isLink, err := v.link(strings.Join(cur, "/"))
		if err != nil {
			return nil, false
		}
		if !isLink {
			continue
		}
		if isAbs(target) {
			return nil, false
		}
		if cur, ok = v.resolve(cur[:len(cur)-1], target, depth+1); !ok {
			return nil, false
		}
	}
	return cur, true
}

// link reports whether name is a symlink: the stream's own entries first,
// then the disk.
func (v *Validator) link(name string) (string, bool, error) {
	if target, ok := v.seen[name]; ok {
		return target, target != "", nil
	}
	if v.lookup == nil {
		return "", false, nil
	}
	return v.lookup(name)
}
