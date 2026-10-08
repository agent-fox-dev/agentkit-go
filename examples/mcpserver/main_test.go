package main_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/agentfox/agentkit-go/mcp"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestTheReferenceBinaryServesOverRealStdio drives the demonstration binary as a
// subprocess with the SHIPPED client.
//
// Everything else exercises the server over in-memory pipes, which cannot
// catch the failures specific to a real process: a stray write to stdout
// corrupting the frame stream, ServeStdio wiring the wrong file descriptors,
// or a signal handler that never lets the process exit. This is the only test
// that runs the binary the way an MCP host would launch it.
func TestTheReferenceBinaryServesOverRealStdio(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	bin := build(t)

	pool := mcp.NewPool(mcp.ConnectionOptions{})
	t.Cleanup(func() { _ = pool.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	conn, err := pool.Connect(ctx, mcp.ServerConfig{Name: "ref", Command: bin}, nil, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	sess, err := conn.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info := sess.InitializeResult().ServerInfo; info == nil || info.Name != "agentkit-mcp-server" {
		t.Fatalf("server identity = %+v", info)
	}

	tools, err := conn.ListTools(ctx)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if len(tools) == 0 {
		t.Fatal("the reference binary registers at least one tool")
	}

	res, err := conn.Call(ctx, "echo", map[string]any{"message": "over a real pipe"})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if tc, ok := res.Content[0].(*mcp.TextContent); res.IsError || !ok || tc.Text != "over a real pipe" {
		t.Fatalf("echo returned %+v", res)
	}

	// The templated resource: the only path that proves URI variables survive
	// a real transport.
	read, err := sess.ReadResource(ctx, &sdk.ReadResourceParams{URI: "agentkit://echo/hello"})
	if err != nil {
		t.Fatalf("resources/read: %v", err)
	}
	if len(read.Contents) != 1 || read.Contents[0].Text != "hello" {
		t.Fatalf("the template variable did not reach the handler: %+v", read.Contents)
	}
}

func build(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "mcpserver")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Env = os.Environ()
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, msg)
	}
	return out
}
