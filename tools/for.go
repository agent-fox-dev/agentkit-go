package tools

import (
	"errors"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// For returns the built-in tools for ws: read_file, write_file, edit_file,
// list_files, find_files, search_files, file_outline, find_symbol,
// find_references, execute and run_command, in that order, followed by
// Options.Index's tools when an index is set. The shell tools run in
// ws.Root(), and ignore configuration is whatever ws was opened with.
//
// A nil ws returns no tools and an error: there is no fallback to the working
// directory, because a silent default is the kind of hidden choice taking the
// workspace as an argument exists to remove.
func For(ws *workspace.Workspace, opts Options) ([]core.Tool, error) {
	if ws == nil {
		return nil, errors.New("tools: For needs a workspace; open one with workspace.Open")
	}
	opts.Workspace = bridge(ws)
	opts.Ignore = IgnoreOptions(ws.Ignore())
	return All(opts)
}

// bridge is the Workspace the tool code still runs on, over ws: the same
// root, with containment answered by ws itself so that the tools report
// workspace's errors.
func bridge(ws *workspace.Workspace) *Workspace {
	return &Workspace{Root: ws.Root(), ws: ws}
}
