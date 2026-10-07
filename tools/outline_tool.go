package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/outline"
	"github.com/agentfox/agentkit-go/schema"
)

// fileOutlineTool returns the file_outline tool.
func (f *fileTools) fileOutlineTool() core.Tool {
	return core.Tool{
		Name: "file_outline",
		Description: "Show the declarations in a file with line ranges. " +
			"Use include_private to also list unexported declarations.",
		Builtin: true,
		InputSchema: schema.Object(
			schema.Prop("path", schema.String("Path to the file (relative to the workspace, or absolute)")),
			schema.Opt("include_private", schema.Bool("Include unexported declarations (default false)")),
		),
		PromptGuidelines: []string{
			"Before reading a large file, outline it and read only the range you need.",
		},
		Execute: func(ctx context.Context, in json.RawMessage) core.ToolResult {
			// Check context cancellation first.
			if ctx.Err() != nil {
				return core.ErrResult("aborted", "Operation aborted")
			}

			var a struct {
				Path           string `json:"path"`
				IncludePrivate bool   `json:"include_private"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return core.ErrResult("invalid_arguments", err.Error())
			}
			if a.Path == "" {
				return core.ErrResult("invalid_arguments", "path is required")
			}

			abs, err := f.ws.Resolve(a.Path)
			if err != nil {
				return core.ErrResult("path_not_allowed", err.Error())
			}

			fi, err := os.Stat(abs)
			if err != nil {
				return core.ErrResult("read_failed", err.Error())
			}
			if !fi.Mode().IsRegular() {
				return core.ErrResult("not_a_file", notAFile(f.ws.Rel(abs), fi.Mode()))
			}

			// Read the file from disk — never from the symbol table — unless
			// its stat already says it is over outline's limit: then outline
			// is handed no source and returns none without reading it either.
			var src []byte
			if fi.Size() <= defaultMaxFileBytes {
				if src, err = os.ReadFile(abs); err != nil {
					return core.ErrResult("outline_failed", err.Error())
				}
			}

			ofile, err := outline.Outline(ctx, abs, src, outline.Options{
				Root:   f.ws.Root,
				Runner: f.outlineRunner(),
			})
			if err != nil {
				// A context cancellation during outline is aborted.
				if ctx.Err() != nil {
					return core.ErrResult("aborted", "Operation aborted")
				}
				return core.ErrResult("outline_failed", err.Error())
			}

			// Render the outline as compact text with line ranges.
			return renderOutlineResult(f.ws, abs, ofile, a.IncludePrivate, src, fi.Size())
		},
	}
}

// renderOutlineResult builds the ToolResult for file_outline.
func renderOutlineResult(ws *Workspace, abs string, ofile outline.File, includePrivate bool, src []byte, size int64) core.ToolResult {
	rel := ws.Rel(abs)
	shown := filepath.ToSlash(rel)

	// Count total and filter declarations.
	total := len(ofile.Decls)
	var listed []outline.Decl
	for _, d := range ofile.Decls {
		if includePrivate || d.Exported {
			listed = append(listed, d)
		}
	}
	unexported := total - len(listed)

	// Build header.
	var b strings.Builder
	b.WriteString(shown)
	b.WriteString("  (")
	b.WriteString(string(ofile.Backend))
	b.WriteString(", ")
	b.WriteString(itoa(total))
	if total == 1 {
		b.WriteString(" declaration")
	} else {
		b.WriteString(" declarations")
	}
	if unexported > 0 && !includePrivate {
		b.WriteString(", ")
		b.WriteString(itoa(unexported))
		b.WriteString(" unexported not listed")
	}
	b.WriteByte(')')

	// Declaration lines.
	if len(listed) == 0 {
		b.WriteByte('\n')
		if ofile.Backend == outline.BackendNone {
			b.WriteString("  No declarations found")
			reason := noneReason(ofile, src, size)
			if reason != "" {
				b.WriteString(" (")
				b.WriteString(reason)
				b.WriteByte(')')
			}
			b.WriteString("; use read_file or search_files")
		} else {
			b.WriteString("  No declarations found")
		}
	} else {
		for _, d := range listed {
			b.WriteByte('\n')
			indent := "  "
			if d.Container != "" && !strings.Contains(d.Signature, d.Container) {
				indent = "    "
			}
			b.WriteString(indent)
			b.WriteString(lineRange(d))
			b.WriteString("  ")
			b.WriteString(d.Signature)
		}
	}

	text := b.String()

	data := map[string]any{
		"file":         ofile,
		"backend":      string(ofile.Backend),
		"declarations": total,
		"listed":       len(listed),
	}

	r := core.OKResult(data)
	r.Text = text
	return r
}

// lineRange formats the L<start>-<end> or L<start> range.
func lineRange(d outline.Decl) string {
	if d.EndLine == 0 || d.EndLine == d.StartLine {
		return "L" + itoa(d.StartLine)
	}
	return "L" + itoa(d.StartLine) + "-" + itoa(d.EndLine)
}

// noneReason infers why the backend is none from the File fields, the file's
// size and the source bytes. src is nil when the file was too large to read.
func noneReason(f outline.File, src []byte, size int64) string {
	if f.Lang == "" {
		return "language not recognised"
	}
	// Check file size against the outline package's default limit.
	if size > defaultMaxFileBytes {
		return "file too large"
	}
	// Check for binary content: NUL byte in the first 8 KiB.
	sniff := src
	if len(sniff) > binarySniffSize {
		sniff = sniff[:binarySniffSize]
	}
	if len(sniff) > 0 {
		for _, b := range sniff {
			if b == 0 {
				return "binary"
			}
		}
	}
	return ""
}

// Constants mirrored from outline to avoid exporting them.
const (
	defaultMaxFileBytes = 1 << 20  // 1 MiB
	binarySniffSize     = 8 * 1024 // 8 KiB
)

// itoa is a minimal int-to-string without importing strconv.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	if n < 0 {
		return "-" + itoa(-n)
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
