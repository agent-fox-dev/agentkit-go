package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// For returns the built-in tools for ws: read_file, write_file, edit_file,
// list_files, find_files, search_files, file_outline, find_symbol,
// find_references, execute and run_command, in that order, followed by
// Options.Index's tools when an index is set. The shell tools run in
// ws.Root(), and every directory walk the tools make uses ws's ignore rules.
//
// A nil ws returns no tools and an error: there is no fallback to the working
// directory, because a silent default is the kind of hidden choice taking the
// workspace as an argument exists to remove.
func For(ws *workspace.Workspace, opts Options) ([]core.Tool, error) {
	if ws == nil {
		return nil, errors.New("tools: For needs a workspace; open one with workspace.Open")
	}
	opts = opts.withDefaults(ws)
	fs := newFileTools(ws, opts)

	// Wrap the shell tools so they mark the symbol table for revalidation
	// after the command returns, whatever the outcome, and call
	// Index.Invalidate("") when an index is set.
	wrapShell := func(t core.Tool) core.Tool {
		orig := t.Execute
		t.Execute = func(ctx context.Context, in json.RawMessage) core.ToolResult {
			r := orig(ctx, in)
			fs.markTableRevalidateAll()
			if fs.index != nil {
				fs.index.Invalidate("")
			}
			return r
		}
		return t
	}

	builtins := []core.Tool{
		fs.readFile(),
		fs.writeFile(),
		fs.editFile(),
		fs.listFiles(),
		fs.findFiles(),
		fs.searchFiles(),
		fs.fileOutlineTool(),
		fs.findSymbolTool(),
		fs.findReferencesTool(),
		wrapShell(executeTool(ws, opts)),
		wrapShell(runCommandTool(ws, opts)),
	}

	// Append index tools when an index is set.
	if opts.Index != nil {
		names := make(map[string]bool, len(builtins))
		for _, t := range builtins {
			names[t.Name] = true
		}
		for _, t := range opts.Index.Tools() {
			if names[t.Name] {
				return nil, fmt.Errorf("tools: index tool %q duplicates a built-in tool name", t.Name)
			}
			builtins = append(builtins, t)
		}
	}

	return builtins, nil
}
