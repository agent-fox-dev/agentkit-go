package workspace

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// WalkOptions configures Walk.
type WalkOptions struct {
	// IncludeHidden visits dot-prefixed entries too. They are skipped by
	// default: hidden entries are a walk option, not an ignore rule.
	IncludeHidden bool
}

// Walk is the workspace's directory walk: the one place a directory tree is
// listed for find_files, the search tools, the symbol table, the reference
// engine and the code-search index. It always applies the workspace's own
// ignore rules.
//
// sub names where the walk starts. "" and "." mean the root, a relative sub
// is joined to the root and an absolute one is taken as it is. sub is then
// cleaned and must lie within the root, compared segment by segment;
// otherwise Walk returns an error wrapping ErrPathNotAllowed before it calls
// fn, and it returns the same error on a nil receiver. sub is neither
// normalized nor symlink-resolved: a caller holding a model-supplied path
// passes it through Resolve first, as find_files does.
//
// fn is called for every file and directory under sub, with rel slash-separated
// and relative to sub, except the entries the rules exclude and, unless
// opts.IncludeHidden is set, dot-prefixed ones. The ignore engine is rooted at
// sub: ignore files above sub do not apply, while the global layer does, and
// so does the repository exclude file of sub when sub is itself a repository
// root.
//
// Symlinks are not followed, and unreadable entries are skipped without error.
// A sub that does not exist, or that is a file, yields no calls and a nil
// error. The context is checked for every entry, and its error is returned.
// fn may return filepath.SkipDir to skip a directory and filepath.SkipAll to
// stop the walk without error; any other error stops the walk and is returned.
func (w *Workspace) Walk(ctx context.Context, sub string, opts WalkOptions, fn func(rel string, d fs.DirEntry) error) error {
	if w == nil {
		return fmt.Errorf("%w: workspace is nil", ErrPathNotAllowed)
	}
	root, err := w.walkRoot(sub)
	if err != nil {
		return err
	}
	return walk(ctx, root, w.ignore, opts.IncludeHidden, fn)
}

// walkRoot is the absolute, cleaned directory sub names, checked lexically.
func (w *Workspace) walkRoot(sub string) (string, error) {
	if sub == "" || sub == "." {
		return w.root, nil
	}
	p := sub
	if !filepath.IsAbs(p) {
		p = filepath.Join(w.root, p)
	}
	p = filepath.Clean(p)
	if !within(w.root, p) {
		return "", fmt.Errorf("%w: %q is outside the workspace root %s"+pathNextStep, ErrPathNotAllowed, sub, w.root)
	}
	return p, nil
}

// walk is the single directory-walk implementation.
//
// It builds a layered ignore engine, computes slash-separated paths relative
// to root, skips entries the ignore engine matches and (when includeHidden is
// false) dot-prefixed entries, calls fn for every surviving entry (files and
// directories), and calls ig.enter for each directory fn did not skip.
//
// Unreadable entries are skipped without error. A root that does not exist
// yields no calls and a nil error. The context is checked per entry.
func walk(ctx context.Context, root string, igOpts IgnoreOptions, includeHidden bool, fn func(rel string, d fs.DirEntry) error) error {
	// A root that does not exist, or cannot be read, yields no calls and a
	// nil error.
	if _, err := os.Stat(root); err != nil {
		return nil
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
