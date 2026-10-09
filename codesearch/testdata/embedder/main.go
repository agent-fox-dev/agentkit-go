// Command embedder is the embedder code path the codesearch README shows, with
// no build constraint: it must compile on every supported platform, so an
// embedder needs no per-platform file to opt in. It is built, never run, by
// TestEmbedderCompilesOnEveryTarget.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/agent-fox-dev/agentkit-go/codesearch"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

func main() {
	ws, err := tools.NewWorkspace(".")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	opts := tools.Options{Workspace: ws}
	idx, err := codesearch.New(ws, codesearch.Options{Ignore: opts.Ignore})
	switch {
	case errors.Is(err, codesearch.ErrUnsupported):
		// Run without code search: Options.Index stays nil.
	case err != nil:
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	default:
		defer idx.Close()
		opts.Index = idx
		// The counters and statistics the README documents, through the
		// same assertion on every platform (the Windows stub has them too).
		if ci, ok := idx.(*codesearch.Index); ok {
			_ = ci.Build(context.Background())
			st := ci.BuildStats()
			fmt.Println(ci.BuildCount(), ci.OverlayBuildCount(), ci.RevalCount(),
				st.FilesIndexed, len(ci.IndexedFiles()), ci.RunDir())
		}
	}

	if _, err := tools.All(opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
