//go:build !windows

package codesearch

// Zoekt imports — these ensure the packages are in the dependency graph.
// They will be replaced by real usage in later tasks.
import (
	_ "github.com/sourcegraph/zoekt"
	_ "github.com/sourcegraph/zoekt/index"
	_ "github.com/sourcegraph/zoekt/query"
	_ "github.com/sourcegraph/zoekt/search"
)
