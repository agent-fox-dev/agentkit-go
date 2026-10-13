package rsyncx

import (
	"errors"
	"io/fs"
	"os"
	"path"
)

// DeleteAbsent removes, under the receiving directory root, every entry the
// sender's transfer set lacks and no rule protects, through an os.Root and
// never following a symlink. A directory the sender lacks is removed only
// once nothing protected is left inside it. It returns the number of entries
// removed.
func DeleteAbsent(root string, m *Matcher, present []string) (int, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return 0, err
	}
	defer r.Close()
	have := make(map[string]bool, len(present))
	for _, p := range present {
		have[path.Clean(p)] = true
	}
	_, n, err := deleteIn(r, ".", m, have)
	return n, err
}

// deleteIn handles the entries of dir and reports whether anything remains
// in it.
func deleteIn(r *os.Root, dir string, m *Matcher, have map[string]bool) (keep bool, n int, err error) {
	ents, err := fs.ReadDir(r.FS(), dir)
	if errors.Is(err, fs.ErrPermission) {
		// A directory we may not read is left alone, as its rule says.
		return true, 0, nil
	} else if err != nil {
		return true, 0, err
	}
	for _, e := range ents {
		p := path.Join(dir, e.Name())
		if m.Excluded(p) {
			keep = true
			continue
		}
		if e.IsDir() {
			k, c, err := deleteIn(r, p, m, have)
			n += c
			if err != nil {
				return true, n, err
			}
			if k || have[p] {
				keep = true
				continue
			}
		} else if have[p] {
			keep = true
			continue
		}
		if err := r.Remove(p); err != nil {
			return true, n, err
		}
		n++
	}
	return keep, n, nil
}
