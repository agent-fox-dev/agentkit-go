package tools

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// WalkOptions configures the exported Walk.
type WalkOptions struct {
	Ignore        IgnoreOptions
	IncludeHidden bool
}

// Walk is the workspace-confined entry point for the shared directory walk.
// It returns an error wrapping ErrPathNotAllowed when ws is nil, root is not
// absolute, or root lies outside ws.Root, before any entry is visited.
// It does not follow symlinks, because it uses filepath.WalkDir.
func Walk(ctx context.Context, ws *Workspace, root string, opts WalkOptions, fn func(rel string, d fs.DirEntry) error) error {
	if ws == nil {
		return fmt.Errorf("%w: workspace is nil", ErrPathNotAllowed)
	}
	if !filepath.IsAbs(root) {
		return fmt.Errorf("%w: root %q is not absolute", ErrPathNotAllowed, root)
	}
	if !within(ws.Root, root) {
		return fmt.Errorf("%w: root %q is outside workspace %q", ErrPathNotAllowed, root, ws.Root)
	}
	return walk(ctx, root, opts.Ignore, opts.IncludeHidden, fn)
}

// walk is the single unexported directory-walk implementation used by
// find_files, the native search_files backend and CountCandidates.
//
// It builds a layered ignore engine, computes slash-separated paths relative
// to root, skips entries the ignore engine matches and (when includeHidden is
// false) dot-prefixed entries, calls fn for every surviving entry (files and
// directories), and calls ig.enter for each directory fn did not skip.
//
// Unreadable entries are skipped without error. A root that does not exist
// yields no calls and a nil error. The context is checked per entry.
func walk(ctx context.Context, root string, igOpts IgnoreOptions, includeHidden bool, fn func(rel string, d fs.DirEntry) error) error {
	// A non-existent root yields no calls and a nil error.
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return nil // unreadable root: treat like missing
	}

	ig := newIgnoreEngine(root, igOpts)

	return filepath.WalkDir(root, func(abs string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// Unreadable entries are skipped without error.
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Check context per entry.
		if err := ctx.Err(); err != nil {
			return err
		}

		// Skip the root entry itself.
		rel, err := filepath.Rel(root, abs)
		if err != nil || rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)

		isDir := d.IsDir()

		// Skip hidden entries when not included.
		if !includeHidden && isHidden(d.Name()) {
			if isDir {
				return filepath.SkipDir
			}
			return nil
		}

		// Skip entries the ignore engine matches.
		if ig.match(rel, isDir) {
			if isDir {
				return filepath.SkipDir
			}
			return nil
		}

		// Call the callback.
		cbErr := fn(rel, d)

		if isDir {
			switch {
			case cbErr == filepath.SkipDir:
				return filepath.SkipDir
			case cbErr == filepath.SkipAll:
				return filepath.SkipAll
			case cbErr != nil:
				return cbErr
			default:
				// Load this directory's own ignore files before its
				// children are matched.
				ig.enter(rel, abs)
				return nil
			}
		}

		// For files: SkipAll ends the walk with nil error (handled by
		// filepath.WalkDir), any other error is returned.
		if cbErr == filepath.SkipAll {
			return filepath.SkipAll
		}
		return cbErr
	})
}
