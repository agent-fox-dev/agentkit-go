package workspace

import "strings"

// isHidden reports whether name is a dot-prefixed entry name. "." and ".." are
// not hidden entries. tools keeps its own copy: the packages share no helper,
// so workspace imports nothing of the module.
func isHidden(name string) bool {
	return strings.HasPrefix(name, ".") && name != "." && name != ".."
}
