package confine

import (
	"fmt"
	"path/filepath"
)

// UsageError is a refused flag value: the command exits 2 with it, applying
// nothing and serving nothing.
type UsageError struct {
	Flag, Reason string
}

func (e *UsageError) Error() string { return e.Flag + ": " + e.Reason }

// validate refuses what would widen the boundary through a flag: a
// --workdir or --landlock-rw that is /, or equal to or an ancestor of a
// system directory, and a relative --landlock-ro or --landlock-rw. A flag is
// a programmer's intent, so these are loud; an unusable HOME or temporary
// directory comes from an image and is only skipped (buildRules).
func validate(in Inputs) error {
	for _, f := range []struct {
		flag  string
		paths []string
		rw    bool
	}{{"--workdir", []string{in.Workdir}, true}, {"--landlock-rw", in.RW, true}, {"--landlock-ro", in.RO, false}} {
		for _, p := range f.paths {
			if f.flag != "--workdir" && !filepath.IsAbs(p) {
				return &UsageError{Flag: f.flag, Reason: fmt.Sprintf("%q is a relative path", p)}
			}
			if !f.rw {
				continue
			}
			// A relative --workdir is judged by the directory it names:
			// from /, "." is / itself.
			r, err := filepath.Abs(p)
			if err != nil {
				return &UsageError{Flag: f.flag, Reason: fmt.Sprintf("%q: %v", p, err)}
			}
			if e, err := filepath.EvalSymlinks(r); err == nil {
				r = e
			}
			if d := systemAncestor(r); d != "" {
				return &UsageError{Flag: f.flag, Reason: fmt.Sprintf("%q resolves to %s, which is / or contains the system directory %s", p, r, d)}
			}
		}
	}
	return nil
}
