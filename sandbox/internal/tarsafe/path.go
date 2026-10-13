// Package tarsafe holds the tar entry rules that apply in both directions:
// when the agent extracts a synced tar into the workspace, and when the host
// validates an exported one.
package tarsafe

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// CleanRel checks a workspace-relative, slash-separated path and returns it
// cleaned. "" and "." both mean the root and come back as ".". An absolute
// path, or one with a ".." component after cleaning, is refused with an
// error naming it.
func CleanRel(p string) (string, error) {
	if p == "" || p == "." {
		return ".", nil
	}
	if isAbs(p) {
		return "", fmt.Errorf("path %q is absolute; want a workspace-relative path", p)
	}
	c := path.Clean(p)
	if c == ".." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("path %q leaves the workspace", p)
	}
	return c, nil
}

// isAbs reports whether p is absolute on any platform the module supports.
func isAbs(p string) bool {
	return strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) || filepath.IsAbs(p) || filepath.VolumeName(p) != ""
}
