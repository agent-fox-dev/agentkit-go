package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/codemode"
	"github.com/agent-fox-dev/agentkit-go/codesearch"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/guard"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// settings are the flags every command takes: what to serve and how.
type settings struct {
	dir   string
	guard string
	// tools is --tools as given, nil when the flag is absent.
	tools    *string
	readOnly bool
}

// register adds the flags of every command to fs.
func (s *settings) register(fs *flag.FlagSet) {
	fs.StringVar(&s.dir, "dir", "", "")
	fs.StringVar(&s.guard, "guard", "allow-all", "")
	fs.Func("tools", "", func(v string) error { s.tools = &v; return nil })
	fs.BoolVar(&s.readOnly, "read-only", false, "")
}

// refusal is a command line the command will not serve: a guard it does not
// know, a tool selection that names no tool or leaves none. It exits 2.
type refusal struct{ msg string }

func (r *refusal) Error() string { return r.msg }

// exitCode is the status for an error from open: 2 for a refusal, 1 for a
// failure to build what was asked for.
func exitCode(err error) int {
	var r *refusal
	if errors.As(err, &r) {
		return 2
	}
	return 1
}

// writes are the tools --read-only leaves out.
var writes = []string{"write_file", "edit_file", "execute", "run_command"}

// shellGuard is the guard --guard names: allow-all (the default, since the
// command's place is inside a sandbox, which is the boundary) or restricted.
func (s settings) shellGuard() (core.BeforeToolCall, error) {
	switch s.guard {
	case "", "allow-all":
		return guard.AllowAll, nil
	case "restricted":
		return guard.Restricted(guard.Options{}), nil
	}
	return nil, &refusal{fmt.Sprintf("agentkit-tools: --guard must be allow-all or restricted, not %q", s.guard)}
}

// choose is the part of all, in its order, that --tools and --read-only both
// allow, and whether code_mode is served over it: when --tools is absent or
// names it, and at least one other tool is served.
func (s settings) choose(all []core.Tool) ([]core.Tool, bool, error) {
	var names []string
	for _, t := range all {
		names = append(names, t.Name)
	}
	known := append(slices.Clone(names), "code_mode")
	var named []string
	if s.tools != nil {
		for _, n := range strings.Split(*s.tools, ",") {
			if n = strings.TrimSpace(n); n == "" {
				continue
			}
			if !slices.Contains(known, n) {
				return nil, false, &refusal{fmt.Sprintf("agentkit-tools: --tools names no tool %q; the tools are %s", n, strings.Join(known, ", "))}
			}
			named = append(named, n)
		}
	}
	var chosen []core.Tool
	for _, t := range all {
		if s.tools != nil && !slices.Contains(named, t.Name) {
			continue
		}
		if s.readOnly && slices.Contains(writes, t.Name) {
			continue
		}
		chosen = append(chosen, t)
	}
	codeMode := (s.tools == nil || slices.Contains(named, "code_mode")) && len(chosen) > 0
	if len(chosen) == 0 {
		return nil, false, &refusal{"agentkit-tools: no tool is selected: --tools and --read-only leave nothing to serve"}
	}
	return chosen, codeMode, nil
}

// checkDir resolves the served directory, the working directory when --dir is
// absent, and refuses one that does not exist or is not a directory. It runs
// before anything is built: workspace.Open accepts a root that does not exist.
func (s *settings) checkDir() error {
	if s.dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("agentkit-tools: the working directory: %v", err)
		}
		s.dir = wd
	}
	fi, err := os.Stat(s.dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("agentkit-tools: --dir %s does not exist", s.dir)
	case err != nil:
		return fmt.Errorf("agentkit-tools: --dir %s: %v", s.dir, err)
	case !fi.IsDir():
		return fmt.Errorf("agentkit-tools: --dir %s is not a directory", s.dir)
	}
	return nil
}

// newIndex builds the code_search index of a workspace. It is a variable so a
// test can stand in for a platform where zoekt does not run.
var newIndex = func(ws *workspace.Workspace) (tools.Index, error) {
	return codesearch.New(ws, codesearch.Options{})
}

// served is what a command serves: the runner over the tool set, and the index
// it holds open.
type served struct {
	runner *agentkit.Runner
	index  tools.Index
}

// close releases the index, which removes its temporary directory.
func (s *served) close() {
	if s.index != nil {
		_ = s.index.Close()
	}
}

// open builds the tool set every command serves, in serving order: the tools
// of tools.For, then code_search, then code_mode bound to exactly the others
// that are served, behind a runner with the guard --guard names. --tools and
// --read-only choose from the set. The shell tools run with tools.For's
// default environment, the process's own as tools.ReducedEnv reduces it. A
// platform without zoekt gets the set without code_search and one line on
// stderr.
func open(s settings, stderr io.Writer) (*served, error) {
	shell, err := s.shellGuard()
	if err != nil {
		return nil, err
	}
	ws, err := workspace.Open(s.dir, workspace.Options{})
	if err != nil {
		return nil, fmt.Errorf("agentkit-tools: opening %s: %v", s.dir, err)
	}
	var opts tools.Options
	idx, err := newIndex(ws)
	switch {
	case errors.Is(err, codesearch.ErrUnsupported):
		fmt.Fprintln(stderr, "agentkit-tools: code_search is not served: zoekt does not run on this platform")
	case err != nil:
		return nil, fmt.Errorf("agentkit-tools: the code_search index: %v", err)
	default:
		opts.Index = idx
	}
	out := &served{index: opts.Index}
	ts, err := tools.For(ws, opts)
	if err != nil {
		out.close()
		return nil, fmt.Errorf("agentkit-tools: the tools: %v", err)
	}
	ts, codeMode, err := s.choose(ts)
	if err != nil {
		out.close()
		return nil, err
	}
	if codeMode {
		cm, _, err := codemode.New(ts, codemode.Options{})
		if err != nil {
			out.close()
			return nil, fmt.Errorf("agentkit-tools: code_mode: %v", err)
		}
		ts = append(ts, cm)
	}
	out.runner, err = agentkit.NewRunner(agentkit.Toolset{Tools: ts, Guard: shell})
	if err != nil {
		out.close()
		return nil, fmt.Errorf("agentkit-tools: the runner: %v", err)
	}
	return out, nil
}
