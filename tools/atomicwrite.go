package tools

import (
	"os"
	"path/filepath"
)

// writeFileAtomic replaces the file at path with data so that a reader — or
// a crash — sees either the old content or the new, never a truncated mix.
// os.WriteFile truncates first and writes second; an agent killed between
// the two (a phase timeout, Ctrl-C) leaves the user's file empty or cut off.
//
// An existing file is replaced by writing a temporary file in the same
// directory and renaming it over the original. The rename is on the file's
// REAL path: a symlink at path is followed, as os.WriteFile follows it, and
// stays a symlink; the caller has already confirmed with CheckWriteTarget
// that its target is inside the workspace. The original's permission bits
// are kept. (A hard link to the original keeps the old content: a rename
// replaces the directory entry, not the inode.)
//
// A file that does not exist yet has no content to lose, so it is created
// with os.WriteFile, which also honours the process umask as before. If the
// rename itself fails — a platform that refuses to rename over an open file
// — the write falls back to os.WriteFile rather than failing the tool call.
func writeFileAtomic(path string, data []byte) error {
	target := path
	if real, err := filepath.EvalSymlinks(path); err == nil {
		target = real
	}
	fi, err := os.Stat(target)
	if err != nil || !fi.Mode().IsRegular() {
		return os.WriteFile(path, data, 0o644)
	}

	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".agentkit-*")
	if err != nil {
		return os.WriteFile(path, data, 0o644)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Chmod(tmpName, fi.Mode().Perm()); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, target); err != nil {
		cleanup()
		return os.WriteFile(path, data, 0o644)
	}
	return nil
}
