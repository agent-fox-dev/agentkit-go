// Command codemode shows the code-mode tool: the model writes one Starlark
// script that calls several tools, filters what they return, and hands back
// only the answer.
//
//	go run ./examples/codemode
//
// It runs offline, with no API key: the "model" is provider/faux replaying
// two scripted code_mode calls. The tools are real — list_files, find_files
// and read_file over a temporary workspace, and a stock lookup served by an
// in-process MCP server — and every call a script makes goes through the
// agent's own interceptor and event pipeline.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	agentkit "github.com/agentfox/agentkit-go"
	"github.com/agentfox/agentkit-go/codemode"
	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/mcp"
	"github.com/agentfox/agentkit-go/provider/faux"
	"github.com/agentfox/agentkit-go/tools"
	"github.com/agentfox/agentkit-go/wire"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "codemode:", err)
		os.Exit(1)
	}
}

// listAndFilter is the first script: one call, filtered in the script, so
// the model reads two file names rather than the whole listing. The second
// call fails, and the script handles it as a value.
const listAndFilter = `entries = list_files(path=".")
go_files = [e for e in entries["entries"] if e.endswith(".go")]
print("Go files:", ", ".join(go_files))

missing = read_file(path="CHANGELOG.md")
if is_error(missing):
    print("CHANGELOG.md:", missing.error)
`

// fanOut is the second script: the notes are read concurrently, then the MCP
// tool is asked about two items at once, and the script returns a summary.
const fanOut = `notes = find_files(pattern="**/*.md")["files"]
docs = parallel([call(read_file, path=n) for n in notes])
for name, doc in zip(notes, docs):
    print(name, "->", doc["content"].splitlines()[0])

stock = parallel([call(inventory__stock, item=i) for i in ["apples", "pears"]])

def main():
    return {s["item"]: s["count"] for s in stock}
`

func run(ctx context.Context, w io.Writer) error {
	ws, err := workspace()
	if err != nil {
		return err
	}
	defer os.RemoveAll(ws.Root)

	// The tools a script may call: three built-in navigation tools...
	all, err := tools.All(tools.Options{Workspace: ws})
	if err != nil {
		return err
	}
	var bound []core.Tool
	for _, t := range all {
		switch t.Name {
		case "list_files", "find_files", "read_file":
			bound = append(bound, t)
		}
	}
	// ...and one from an MCP server, imported through a pool as any
	// embedder would import a remote server's tools.
	pool, closeServer, err := inventoryPool(ctx)
	if err != nil {
		return err
	}
	defer closeServer()
	defer pool.Close()
	mcpTools, err := pool.Tools(ctx, bound)
	if err != nil {
		return err
	}
	bound = append(bound, mcpTools...)

	cm, info, err := codemode.New(bound, codemode.Options{SpillDir: filepath.Join(ws.Root, ".spill")})
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "code_mode binds %d tools; its description is %d bytes (%d characters)\n\n",
		info.BoundToolsCount, info.DescriptionBytes, info.DescriptionChars)

	model := faux.New(
		turn(faux.FauxToolCall("call_1", "code_mode", scriptArgs(listAndFilter))),
		turn(faux.FauxToolCall("call_2", "code_mode", scriptArgs(fanOut))),
		faux.Turn{Blocks: []core.ContentBlock{faux.FauxText(
			"The workspace has two Go files, no CHANGELOG.md, and 12 apples and 0 pears in stock.")},
			StopReason: core.StopReasonStop},
	)
	agent, err := agentkit.NewAgent(core.AgentConfig{
		Model:         faux.Model(),
		Providers:     core.ProviderRegistry{faux.API: model.APIProvider()},
		StopPolicy:    func(sc core.StopContext) bool { return sc.TurnCount >= 5 },
		ParallelTools: true,
		ToolPolicy:    core.ToolPolicy{CustomTools: []core.Tool{cm}},
	})
	if err != nil {
		return err
	}
	st, err := agent.Stream(ctx, "What Go files and notes are in the workspace, and how many apples and pears are in stock?")
	if err != nil {
		return err
	}
	// Every call a script makes closes on the agent's event stream with the
	// code_mode call as its parent.
	for e := range st.Events() {
		if end, ok := e.(core.ToolExecutionEndEvent); ok && end.ParentToolUseID != "" {
			fmt.Fprintf(w, "  nested: %s called %s (ok=%v)\n", end.ParentToolUseID, end.Name, !end.IsError)
		}
	}
	res, err := st.RunResult()
	if err != nil {
		return err
	}

	// The transcript holds the two code_mode calls and their results — not
	// the seven calls the scripts made.
	for _, m := range res.Messages {
		r, ok := m.(core.ToolResultMessage)
		if !ok {
			continue
		}
		fmt.Fprintf(w, "\n%s result:\n%s\n", r.ToolName, indent(text(r)))
		if r.IsError {
			return fmt.Errorf("%s failed: %s", r.ToolName, text(r))
		}
	}
	fmt.Fprintf(w, "\nmodel: %s\n\nCode mode example completed successfully\n", res.FinalText())
	return nil
}

// workspace is a small directory for the scripts to explore.
func workspace() (*tools.Workspace, error) {
	dir, err := os.MkdirTemp("", "codemode-example-")
	if err != nil {
		return nil, err
	}
	files := map[string]string{
		"main.go":         "package main\n\nfunc main() {}\n",
		"util.go":         "package main\n\nfunc helper() int { return 1 }\n",
		"README.md":       "# Example project\nA workspace for the code-mode example.\n",
		"docs/NOTES.md":   "Notes: the scripts read these concurrently.\n",
		"assets/logo.txt": "not a picture\n",
	}
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return nil, err
		}
	}
	return tools.NewWorkspace(dir)
}

// inventoryPool serves one MCP tool, stock, from the official SDK's server
// over in-memory pipes and returns a pool connected to it. The tool declares
// an output schema and returns structured content, which a script reads as a
// dict.
func inventoryPool(ctx context.Context) (*mcp.Pool, func(), error) {
	srv := sdk.NewServer(&sdk.Implementation{Name: "inventory", Version: "1"}, nil)
	counts := map[string]int{"apples": 12, "pears": 0}
	srv.AddTool(&mcp.Tool{
		Name:         "stock",
		Description:  "How many of an item are in stock",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"item":{"type":"string","description":"the item"}},"required":["item"]}`),
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"item":{"type":"string"},"count":{"type":"integer"}},"required":["item","count"]}`),
	}, func(_ context.Context, req *sdk.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Item string `json:"item"`
		}
		_ = json.Unmarshal(req.Params.Arguments, &args)
		return &mcp.CallToolResult{
			Content:           []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("%s: %d", args.Item, counts[args.Item])}},
			StructuredContent: map[string]any{"item": args.Item, "count": counts[args.Item]},
		}, nil
	})

	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	ss, err := srv.Connect(ctx, mcp.NewPipeTransport(c2sR, s2cW, wire.Defaults()), nil)
	if err != nil {
		return nil, nil, err
	}
	conn, err := mcp.Connect(ctx, mcp.ServerConfig{Name: "inventory"},
		mcp.NewPipeTransport(s2cR, c2sW, wire.Defaults()), mcp.ConnectionOptions{})
	if err != nil {
		_ = ss.Close()
		return nil, nil, err
	}
	pool := mcp.NewPool(mcp.ConnectionOptions{})
	if err := pool.Add(conn); err != nil {
		_ = ss.Close()
		return nil, nil, err
	}
	return pool, func() { _ = ss.Close() }, nil
}

func turn(b core.ContentBlock) faux.Turn {
	return faux.Turn{Blocks: []core.ContentBlock{b}, StopReason: core.StopReasonToolUse}
}

func scriptArgs(script string) string {
	b, _ := json.Marshal(map[string]string{"script": script})
	return string(b)
}

func text(m core.ToolResultMessage) string {
	var b strings.Builder
	for _, c := range m.Content {
		if t, ok := c.(core.TextBlock); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}

func indent(s string) string { return "  " + strings.ReplaceAll(s, "\n", "\n  ") }
