package rsyncx

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/tarsafe"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// RuleOptions configures one side's rule computation.
type RuleOptions struct {
	// Ignore is the ignore engine's environment for this side.
	Ignore workspace.IgnoreOptions
	// IncludeIgnored turns ignore rules off.
	IncludeIgnored bool
	// ExcludeGit adds the .git rule and skips .git subtrees.
	ExcludeGit bool
	// File marks a transfer of a single regular file: no ignore rules.
	File bool
	// Base, when set and root is a directory inside it, is the tree whose
	// ignore files apply: an export of a workspace subdirectory honours the
	// workspace's ignore rules. A root that Base's rules ignore takes only
	// its own ignore files, since it was named explicitly.
	Base string
}

// RuleSet is one side's exclude rules over its tree, as slash-separated
// paths relative to the transfer root.
type RuleSet struct {
	// Ignored are the entries workspace.Walk does not visit, at their highest
	// ignored level, never under .git.
	Ignored []string
	// Unreadable are the entries the walk cannot read.
	Unreadable []string
	// Escaping are the symlinks whose target leaves the root.
	Escaping []string
	// Git is the .git rule.
	Git bool
}

// List is the sorted, deduplicated rules, the ignored ones only when
// withIgnored, with ".git" when Git is set.
func (r RuleSet) List(withIgnored bool) []string {
	var out []string
	if withIgnored {
		out = append(out, r.Ignored...)
	}
	out = append(append(out, r.Unreadable...), r.Escaping...)
	if r.Git {
		out = append(out, ".git")
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// Compute walks the tree at root without following symlinks and returns its
// rules. An entry it cannot read is a rule, not an error.
func Compute(ctx context.Context, root string, opts RuleOptions) (RuleSet, error) {
	rs := RuleSet{Git: opts.ExcludeGit}
	if opts.File {
		return rs, nil
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return rs, err
	}
	ignoring := !opts.IncludeIgnored
	visited := map[string]bool{".": true}
	if ignoring {
		reached, err := visitFrom(ctx, opts.Base, root, opts.Ignore, visited)
		if err != nil {
			return rs, err
		}
		if !reached {
			if _, err := visitFrom(ctx, "", root, opts.Ignore, visited); err != nil {
				return rs, err
			}
		}
	}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, werr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if werr != nil {
			if rel == "." {
				return werr
			}
			rs.Unreadable = append(rs.Unreadable, rel)
			return skip(d)
		}
		if rel == "." {
			return nil
		}
		inGit := slices.Contains(strings.Split(rel, "/"), ".git")
		if inGit && opts.ExcludeGit {
			return skip(d)
		}
		if !inGit && ignoring && !visited[rel] && visited[path.Dir(rel)] {
			rs.Ignored = append(rs.Ignored, rel)
			return skip(d)
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				rs.Unreadable = append(rs.Unreadable, rel)
			} else if tarsafe.SymlinkEscapes(rel, target) {
				rs.Escaping = append(rs.Escaping, rel)
			}
		case d.Type().IsRegular():
			f, err := os.Open(p)
			if err != nil {
				rs.Unreadable = append(rs.Unreadable, rel)
				return nil
			}
			_ = f.Close()
		}
		return nil
	})
	if err != nil {
		return RuleSet{}, err
	}
	for _, l := range [][]string{rs.Ignored, rs.Unreadable, rs.Escaping} {
		slices.Sort(l)
	}
	return rs, nil
}

// visitFrom marks in visited, relative to root, every entry under root that
// workspace.Walk visits from base (root when base is empty or does not contain
// root). It reports whether the walk reached root.
func visitFrom(ctx context.Context, base, root string, ig workspace.IgnoreOptions, visited map[string]bool) (bool, error) {
	prefix := ""
	if base != "" {
		b, err := filepath.EvalSymlinks(base)
		if err != nil {
			return false, err
		}
		if r, err := filepath.Rel(b, root); err == nil && r != "." && filepath.IsLocal(r) {
			base, prefix = b, filepath.ToSlash(r)
		}
	}
	if prefix == "" {
		base = root
	}
	ws, err := workspace.Open(base, workspace.Options{Ignore: ig})
	if err != nil {
		return false, err
	}
	reached := prefix == ""
	err = ws.Walk(ctx, "", workspace.WalkOptions{IncludeHidden: true}, func(rel string, _ fs.DirEntry) error {
		switch {
		case prefix == "":
			visited[rel] = true
		case rel == prefix:
			reached = true
		case strings.HasPrefix(rel, prefix+"/"):
			visited[rel[len(prefix)+1:]] = true
		}
		return nil
	})
	return reached, err
}

// skip skips a directory's subtree, and nothing for a file (fs.SkipDir on a
// file would skip its later siblings).
func skip(d fs.DirEntry) error {
	if d != nil && d.IsDir() {
		return fs.SkipDir
	}
	return nil
}

// Combine is the sorted, deduplicated union of two sides' rules.
func Combine(host, plan []string) []string {
	out := append(slices.Clone(host), plan...)
	slices.Sort(out)
	return slices.Compact(out)
}

// CheckRuleSize refuses rules, or a transfer set, over MaxRuleBytes.
func CheckRuleSize(rules []string) error {
	n := 0
	for _, r := range rules {
		n += len(r)
	}
	if n > MaxRuleBytes {
		return fmt.Errorf("rsync rules measure %d bytes, over the 8 MiB limit (%d bytes)", n, MaxRuleBytes)
	}
	return nil
}

// Matcher decides which paths a rule list excludes: a rule matches its own
// path and everything under it, character for character (wildcards are
// literal), and ".git" matches a .git component at any depth.
type Matcher struct {
	set map[string]bool
	git bool
}

// NewMatcher builds a Matcher over rules.
func NewMatcher(rules []string) *Matcher {
	m := &Matcher{set: map[string]bool{}}
	for _, r := range rules {
		if r == ".git" {
			m.git = true
			continue
		}
		m.set[path.Clean(r)] = true
	}
	return m
}

// Excluded reports whether the slash-separated relative path p, or one of
// its ancestors, is excluded.
func (m *Matcher) Excluded(p string) bool {
	for p = path.Clean(p); p != "." && p != "/" && p != ""; p = path.Dir(p) {
		if m.set[p] || m.git && path.Base(p) == ".git" {
			return true
		}
	}
	return false
}
