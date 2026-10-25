// Package workspace is the root every AgentKit scenario opens first: the
// containment boundary for the file tools, the ignore rules that apply under
// it and the directory walk that honours them.
//
// Open turns a directory into a *Workspace. The built-in tools
// (tools.For), the code-search index and the sandbox tree are all built for
// one, so ignore configuration is given once, to Open.
//
// The package imports only the standard library and doublestar, so every
// other package of the module can depend on it.
package workspace

import (
	"os"
	"path/filepath"
)

// Options configures Open.
type Options struct {
	// Ignore injects the environment the ignore engine reads. The zero value
	// reads the real one, global excludes included; NoGlobalExcludes() pins
	// an empty global layer.
	Ignore IgnoreOptions
}

// Workspace is the containment boundary for the built-in FILE tools, with the
// ignore rules that apply under its root.
//
// On a host it does NOT constrain `execute`. That is not an oversight, and
// it is stated here rather than left to be discovered: a shell command can
// read and write anything the process can, and pretending otherwise would be
// worse than admitting it. The boundary for `execute` is the BeforeToolCall
// interceptor. See TestExecuteIsNotPathContained in package tools, whose name
// is the documentation.
//
// Inside a sandbox whose agent reports a Landlock ABI
// (sandbox.AgentInfo.Landlock), the agent's kernel ruleset confines
// `execute`, and every process it starts, to the ruleset's writable roots.
// Under the local provider, or on a kernel without Landlock, it does not.
//
// A Workspace is immutable after Open and safe for concurrent use. One not
// obtained from Open is unsupported.
type Workspace struct {
	// root is the absolute, symlink-resolved workspace root.
	root string
	// ignore is the workspace's ignore environment with the `git config`
	// lookup memoized, so the whole workspace runs at most one.
	ignore IgnoreOptions
}

// Open resolves root to its absolute canonical form. An empty root means the
// process working directory. A root that does not exist yet is kept in its
// lexical absolute form and is not an error: containment still works against
// it, and the sandbox and the tests open roots that are created later.
//
// Open reads no ignore file and starts no subprocess: the core.excludesFile
// lookup is memoized and runs, at most once, when the rules are first used.
// It fails only when the working directory or the absolute path cannot be
// determined.
func Open(root string, opts Options) (*Workspace, error) {
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		root = wd
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	// Resolve the root once. If it does not exist yet that is fine; containment
	// still works against the lexical form.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return &Workspace{root: abs, ignore: opts.Ignore.memoized()}, nil
}

// Root returns the absolute, symlink-resolved workspace root.
func (w *Workspace) Root() string { return w.root }

// Ignore returns the workspace's ignore options, with the core.excludesFile
// lookup memoized, so they can be passed on (to sandbox.LocalDir.Ignore, say)
// and used without paying for another lookup.
func (w *Workspace) Ignore() IgnoreOptions { return w.ignore }
