package landlock

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// removeTree removes dir and everything beneath it without following any
// symlink. It restores owner read, write and execute on each directory before
// reading it, so read-only trees (a Go module cache, a chmod -R a-w) do not
// stop it. A missing dir is not an error.
func removeTree(dir string) error {
	fi, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return os.Remove(dir)
	}
	if err := os.Chmod(dir, fi.Mode().Perm()|0o700); err != nil {
		return err
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range ents {
		errs = append(errs, removeTree(filepath.Join(dir, e.Name())))
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	return os.Remove(dir)
}

// emptyDir removes dir's contents with removeTree, keeping dir itself (with
// its owner rwx restored). A missing dir is not an error.
func emptyDir(dir string) error {
	fi, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.Chmod(dir, fi.Mode().Perm()|0o700); err != nil {
		return err
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range ents {
		errs = append(errs, removeTree(filepath.Join(dir, e.Name())))
	}
	return errors.Join(errs...)
}
