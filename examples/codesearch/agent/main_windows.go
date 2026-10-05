//go:build windows

package main

import (
	"fmt"
	"os"

	"github.com/agentfox/agentkit-go/codesearch"
)

// zoekt does not build on Windows, so the codesearch package there is a stub
// whose New returns ErrUnsupported. This file keeps
// the example buildable on every platform and says why it does nothing.
func main() {
	fmt.Fprintln(os.Stderr, "this example needs code search, which is unavailable on Windows:", codesearch.ErrUnsupported)
	os.Exit(1)
}
