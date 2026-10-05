// Command embedder is the embedder code path the codesearch README shows, with
// no build constraint: it must compile on every supported platform, so an
// embedder needs no per-platform file to opt in. It is built, never run, by
// TestEmbedderCompilesOnEveryTarget.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/agentfox/agentkit-go/codesearch"
	"github.com/agentfox/agentkit-go/tools"
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
	}

	if _, err := tools.All(opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
