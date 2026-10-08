// Command code_search calls the code_search tool the way an agent calls it for
// a model, and prints what the model would read. It is the codesearch
// counterpart of the programs in examples/tools (see examples/tools/README.md).
//
//	cd examples/codesearch
//	go run ./code_search -schema
//	go run ./code_search -dir ../.. '{"query":"sym:NewWorkspace"}'
//
// The index is built the way an agent gets it: codesearch.New on the
// workspace, handed to tools.All through tools.Options.Index, which appends
// code_search to the built-in set. The first query builds the index. On
// Windows codesearch.New returns codesearch.ErrUnsupported and the program
// says so.
package main

import (
	"io"
	"log"

	"github.com/agentfox/agentkit-go/codesearch"
	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/examples/tools/toolcli"
	"github.com/agentfox/agentkit-go/tools"
)

func main() { toolcli.MainWith("code_search", withIndex) }

func withIndex(ws *tools.Workspace) ([]core.Tool, func(), error) {
	// zoekt logs shard builds through the standard logger; stdout and stderr
	// are this program's output, so keep them quiet.
	log.SetOutput(io.Discard)

	index, err := codesearch.New(ws, codesearch.Options{})
	if err != nil {
		return nil, nil, err
	}
	ts, err := tools.All(tools.Options{Workspace: ws, Index: index})
	if err != nil {
		_ = index.Close()
		return nil, nil, err
	}
	// Close deletes the index's shard directory under TempDir.
	return ts, func() { _ = index.Close() }, nil
}
