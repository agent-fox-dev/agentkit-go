package landlock

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// resolveRoot is Options.Root (or the user cache default) made absolute,
// created with mode 0700 when missing, and resolved through symlinks once.
func resolveRoot(given string) (string, error) {
	root := given
	if root == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("landlock: Options.Root is empty and there is no user cache directory: %w", err)
		}
		root = filepath.Join(cache, "agentkit-sandbox-landlock")
	}
	refuse := func(err error) error { return fmt.Errorf("landlock: Options.Root %q: %w", root, err) }
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", refuse(err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return "", refuse(err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", refuse(err)
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return "", refuse(err)
	}
	if !fi.IsDir() {
		return "", refuse(errors.New("not a directory"))
	}
	return resolved, nil
}

// checkExtra checks each ExtraRO or ExtraRW entry (field names which) and
// returns them resolved where they exist and cleaned otherwise. An entry must
// be absolute, not /, and neither equal, contain nor lie beneath root.
func checkExtra(which string, paths []string, root string) ([]string, error) {
	out := make([]string, 0, len(paths))
	for i, p := range paths {
		name := fmt.Sprintf("Options.%s[%d]", which, i)
		if !filepath.IsAbs(p) || filepath.Clean(p) == "/" {
			return nil, fmt.Errorf("landlock: %s %q must be an absolute directory other than /", name, p)
		}
		r, err := filepath.EvalSymlinks(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			r = filepath.Clean(p)
		case err != nil:
			return nil, fmt.Errorf("landlock: %s %q: %w", name, p, err)
		}
		if within(r, root) || within(root, r) {
			return nil, fmt.Errorf("landlock: %s %q overlaps Options.Root %s: one sandbox could reach another", name, p, root)
		}
		out = append(out, r)
	}
	return out, nil
}

// within reports whether p is parent or lies beneath it, comparing cleaned
// paths component by component, so /r-other is not beneath /r.
func within(parent, p string) bool {
	parent, p = filepath.Clean(parent), filepath.Clean(p)
	if p == parent {
		return true
	}
	if parent == string(filepath.Separator) {
		return true
	}
	return strings.HasPrefix(p, parent+string(filepath.Separator))
}
