package tools

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/agentfox/agentkit-go/core"
)

// renderReferencesText formats the human-readable result text for find_references.
// Verifies 05-REQ-7.1, 05-REQ-7.2, 05-REQ-7.3, 05-REQ-7.5.
func renderReferencesText(name string, res ReferenceResult) string {
	if name == "" {
		name = res.Target.Name
	}
	backend := res.Backend
	if backend == "" {
		if res.Target.Name == "" && name == "" {
			backend = "text"
		} else if res.Target.Name == "" {
			backend = "text"
		} else {
			backend = "go/types"
		}
	}

	totalRefs := len(res.Sites)
	fileSet := make(map[string]struct{})
	textMatches := 0
	for _, site := range res.Sites {
		fileSet[site.Path] = struct{}{}
		if site.Confidence == "text" {
			textMatches++
		}
	}
	fileCount := len(fileSet)

	var b strings.Builder
	// 05-REQ-7.1: 'find_references <name>  (<backend>, <N> references in <M> files; <K> text matches)'
	fmt.Fprintf(&b, "find_references %s  (%s, %d references in %d files; %d text matches)", name, backend, totalRefs, fileCount, textMatches)
	if res.Partial {
		b.WriteString(" [partial]")
	}

	// Group sites by file, preserving relative order of appearance in res.Sites.
	type fileGroup struct {
		path  string
		sites []ReferenceSite
	}
	var groups []fileGroup
	groupIndex := make(map[string]int)
	for _, site := range res.Sites {
		slashPath := filepath.ToSlash(site.Path)
		idx, exists := groupIndex[slashPath]
		if !exists {
			idx = len(groups)
			groupIndex[slashPath] = idx
			groups = append(groups, fileGroup{path: slashPath})
		}
		groups[idx].sites = append(groups[idx].sites, site)
	}

	// 05-REQ-7.2: Group by file with slash-separated relative path header and aligned indented site lines.
	for _, g := range groups {
		b.WriteString("\n")
		b.WriteString(g.path)

		maxEnclosingLen := 0
		for _, site := range g.sites {
			label := renderEnclosingLabel(site.Enclosing)
			if len(label) > maxEnclosingLen {
				maxEnclosingLen = len(label)
			}
		}
		encWidth := maxEnclosingLen + 6

		for _, site := range g.sites {
			label := renderEnclosingLabel(site.Enclosing)
			b.WriteString("\n")
			if site.Source != "" {
				fmt.Fprintf(&b, "  L%d  %s  %-*s%s", site.Line, site.Confidence, encWidth, label, site.Source)
			} else {
				fmt.Fprintf(&b, "  L%d  %s  %s", site.Line, site.Confidence, label)
			}
		}
	}

	// 05-REQ-7.3: Append truncation or partial markers on distinct lines at the conclusion of Text.
	if res.Truncated {
		limit := len(res.Sites)
		if limit <= 0 {
			limit = 30
		}
		if limit > 100 {
			limit = 100
		}
		marker := CapMarker("references", "max_results", limit, 100, "narrow with path or kind")
		b.WriteString("\n")
		b.WriteString(marker)
	}
	if res.Partial {
		partialMarker := SymbolPartialMarker("")
		b.WriteString("\n")
		b.WriteString(partialMarker)
	}

	return b.String()
}

// renderResultText formats the human-readable result text for find_references using res.Target.Name.
func renderResultText(res ReferenceResult) string {
	return renderReferencesText(res.Target.Name, res)
}

// renderReferencesResult formats ReferenceResult into a core.ToolResult.
// Verifies 05-REQ-7.4, 05-REQ-7.5.
func renderReferencesResult(name string, res ReferenceResult) core.ToolResult {
	if res.Sites == nil {
		res.Sites = []ReferenceSite{}
	}
	if name == "" {
		name = res.Target.Name
	}
	if res.Backend == "" {
		if res.Target.Name == "" && name == "" {
			res.Backend = "text"
		} else if res.Target.Name == "" {
			res.Backend = "text"
		} else {
			res.Backend = "go/types"
		}
	}

	text := renderReferencesText(name, res)
	data := map[string]any{
		"target":           res.Target,
		"sites":            res.Sites,
		"backend":          res.Backend,
		"partial":          res.Partial,
		"truncated":        res.Truncated,
		"packages_checked": res.PackagesChecked,
		"errors":           res.Errors,
		"result":           res,
	}

	tr := core.OKResult(data)
	tr.Text = text
	if res.Truncated {
		tr.Metadata = &core.ToolMetadata{
			Truncated:   true,
			TruncatedBy: string(TruncatedByLines),
		}
	}
	return tr
}

// renderReferencesToolResult is an alias for renderReferencesResult.
func renderReferencesToolResult(name string, res ReferenceResult) core.ToolResult {
	return renderReferencesResult(name, res)
}

// renderResult formats ReferenceResult into a core.ToolResult using res.Target.Name.
func renderResult(res ReferenceResult) core.ToolResult {
	return renderReferencesResult(res.Target.Name, res)
}
