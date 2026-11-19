package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/codemode"
	"github.com/agent-fox-dev/agentkit-go/codesearch"
	"github.com/agent-fox-dev/agentkit-go/guard"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// settings are the flags every command takes: what to serve and how.
type settings struct {
	dir string
}

// register adds the flags of every command to fs.
func (s *settings) register(fs *flag.FlagSet) {
	fs.StringVar(&s.dir, "dir", "", "")
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
// of tools.For, then code_search, then code_mode bound to all of the others,
// behind a runner. The shell tools run with tools.For's default environment,
// the process's own as tools.ReducedEnv reduces it. A platform without zoekt
// gets the set without code_search and one line on stderr.
func open(s settings, stderr io.Writer) (*served, error) {
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
	cm, _, err := codemode.New(ts, codemode.Options{})
	if err != nil {
		out.close()
		return nil, fmt.Errorf("agentkit-tools: code_mode: %v", err)
	}
	ts = append(ts, cm)
	out.runner, err = agentkit.NewRunner(agentkit.Toolset{Tools: ts, Guard: guard.AllowAll})
	if err != nil {
		out.close()
		return nil, fmt.Errorf("agentkit-tools: the runner: %v", err)
	}
	return out, nil
}
