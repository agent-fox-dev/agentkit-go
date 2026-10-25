package tools

import (
	"fmt"
	"strings"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/outline"
)

// renderReferencesText formats the human-readable result text for find_references.
func renderReferencesText(name string, res ReferenceResult) string {
	files := make(map[string]struct{})
	textMatches := 0
	for _, site := range res.Sites {
		files[site.Path] = struct{}{}
		if site.Confidence == "text" {
			textMatches++
		}
	}

	var b strings.Builder
	// The header: 'find_references <name>  (<backend>, <N> references in <M> files; <K> text matches)'
	fmt.Fprintf(&b, "find_references %s  (%s, %d references in %d files; %d text matches)", name, res.Backend, len(res.Sites), len(files), textMatches)
	// A zero Target means no declaration matched the name.
	if res.Target == (outline.Decl{}) {
		b.WriteString(" [0 declarations matched]")
	}
	if res.Partial {
		b.WriteString(" [partial]")
	}

	// Group by file, in order of first appearance, under a path header with
	// the enclosing labels padded to align.
	var order []string
	groups := make(map[string][]ReferenceSite)
	for _, site := range res.Sites {
		if _, ok := groups[site.Path]; !ok {
			order = append(order, site.Path)
		}
		groups[site.Path] = append(groups[site.Path], site)
	}
	for _, path := range order {
		b.WriteString("\n")
		b.WriteString(path)
		width := 0
		for _, site := range groups[path] {
			width = max(width, len(renderEnclosingLabel(site.Enclosing)))
		}
		for _, site := range groups[path] {
			label := renderEnclosingLabel(site.Enclosing)
			if site.Source != "" {
				fmt.Fprintf(&b, "\n  L%d  %-8s  %-*s%s", site.Line, site.Confidence, width+6, label, site.Source)
			} else {
				fmt.Fprintf(&b, "\n  L%d  %-8s  %s", site.Line, site.Confidence, label)
			}
		}
	}

	// Truncation and partial markers on distinct lines at the end.
	if res.Truncated {
		b.WriteString("\n")
		b.WriteString(CapMarker("references", "max_results", clampMaxResults(len(res.Sites)), 100, "narrow with path or kind"))
	}
	if res.Partial {
		b.WriteString("\n")
		b.WriteString(SymbolPartialMarker(res.partialReason))
	}
	return b.String()
}

// renderReferencesResult formats ReferenceResult into a core.ToolResult.
func renderReferencesResult(name string, res ReferenceResult) core.ToolResult {
	if res.Sites == nil {
		res.Sites = []ReferenceSite{}
	}
	tr := core.OKResult(map[string]any{
		"target":           res.Target,
		"sites":            res.Sites,
		"backend":          res.Backend,
		"partial":          res.Partial,
		"truncated":        res.Truncated,
		"packages_checked": res.PackagesChecked,
		"errors":           res.Errors,
		"result":           res,
	})
	tr.Text = renderReferencesText(name, res)
	if res.Truncated {
		tr.Metadata = &core.ToolMetadata{
			Truncated:   true,
			TruncatedBy: string(TruncatedByLines),
		}
	}
	return tr
}
