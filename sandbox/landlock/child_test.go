package landlock

import (
	"fmt"
	"os"
)

// children are the re-executed test children, by name.
var children = map[string]func() error{}

// runChild runs the named child and returns its exit status.
func runChild(name string) int {
	f, ok := children[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "no child %q\n", name)
		return 2
	}
	if err := f(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
