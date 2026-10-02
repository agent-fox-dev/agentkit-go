//go:build !windows

package codesearch

// Zoekt imports — these ensure the query and search packages are in the
// dependency graph. index.go imports zoekt and zoekt/index directly.
import (
	_ "github.com/sourcegraph/zoekt/query"
	_ "github.com/sourcegraph/zoekt/search"
)
