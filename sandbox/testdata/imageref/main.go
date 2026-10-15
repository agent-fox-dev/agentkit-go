// Command imageref is an embedder program for TS-16-54: it prints the
// published base image reference for its build's version, or the error and
// exits 1.
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
	fmt.Println(base)
}
