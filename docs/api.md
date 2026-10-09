# Network API

AgentKit serves no API. It is a library: it has no MCP server and nothing in
it listens on a socket. Its only network traffic is outbound — the Anthropic
provider's requests, and the MCP client's connections to servers an embedder
configures.

The MCP client (`mcp.Pool`, `mcp.Connect`) is described in
[`architecture.md`](architecture.md), and its configuration keys and limits in
[`configuration.md`](configuration.md).

## Programmatic Workspace reference search

Embedders can query references programmatically without model execution through the exported `Workspace.References` method:

```go
type ReferenceOptions struct {
	Path         string // Subdirectory filter ("" for entire workspace)
	IncludeTests bool   // Whether to include test files (default false in struct, tool defaults to true)
	MaxResults   int    // 0 or negative defaults to 30; capped at 100
}

type ReferenceSite struct {
	Path       string       // Slash-separated workspace-relative path
	Line       int          // 1-based line number
	Column     int          // 1-based byte column
	Confidence string       // "resolved", "lexical", or "text"
	Enclosing  outline.Decl // Enclosing declaration, or Kind "file" at top-level
	Source     string       // Trimmed source line, max 200 bytes, no control chars
}

type ReferenceResult struct {
	Target          outline.Decl
	Sites           []ReferenceSite
	Backend         string
	Partial         bool
	PackagesChecked int
	Errors          int
	Truncated       bool
}

func (ws *Workspace) References(ctx context.Context, target outline.Decl, opts ReferenceOptions) (ReferenceResult, error)
```

`Workspace.References` enforces workspace containment, validates subdirectories, and executes exact Go type resolution and multi-language outline attribution with the same ranking and truncation as the `find_references` tool. Every site's `Enclosing` is the innermost declaration of the file's outline whose line range spans the site, or `Kind: "file"` at top level. The pass is bounded by the default `SymbolOptions` (50 000 files, 2 s); when a bound is reached it returns the sites found so far with `Partial` set and a nil error. Unlike the tool it keeps no cache between calls. `PackagesChecked` counts the Go package type-checks the pass used (a package with test files is checked a second time with them) and `Errors` counts the parse and type errors they collected, most of them from external imports, which are stubbed as empty packages; both are 0 when no Go code was checked.
