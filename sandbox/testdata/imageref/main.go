// Command imageref is an embedder program for TS-16-54: it prints the
// published base and overlay image references for its build's version, one
// per line, or the error and exits 1.
package main

import (
	"fmt"
	"os"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

func main() {
	base, err := sandbox.DefaultImage()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	overlay, err := sandbox.OverlayImage()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(base)
	fmt.Println(overlay)
}
