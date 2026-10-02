//go:build !windows

package codesearch

// Zoekt imports — these ensure the query and search packages are in the
// dependency graph even if tool.go's direct imports are refactored.
import (
	_ "github.com/sourcegraph/zoekt/query"
	_ "github.com/sourcegraph/zoekt/search"
)
