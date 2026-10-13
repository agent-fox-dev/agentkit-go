// Command sandbox shows AgentKit's built-in tools running in a sandbox: an
// ephemeral environment that holds one workspace. Sandbox.Tools returns a
// drop-in replacement for tools.All whose every call runs in the AgentKit
// agent inside the sandbox; the driver does not change.
//
//	go run ./examples/sandbox
//
// It runs offline, with no API key: the "model" is provider/faux replaying
// one scripted code_mode call, and the sandbox comes from local, the
// in-process provider. local is NOT isolation — it runs the agent in this
// process with its privileges, and exists for tests and development — but
// it speaks the same protocol, over the same transport, as a real provider.
package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/codemode"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/guard"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "sandbox:", err)
		os.Exit(1)
	}
}

// project is what the embedder puts into the sandbox: name, then content.
var project = [][2]string{
	{"README.md", "# Greeter\nSays hello from inside the sandbox.\n"},
	{"src/main.go", "package main\n\n// TODO: take the name from the command line.\nfunc main() { println(\"hello\") }\n"},
	{"src/util.go", "package main\n\n// TODO: remove.\nfunc unused() {}\n"},
}

// script is the model's code_mode call: it reads, searches and lists inside
// the sandbox. Its last call is refused by the guard, and the script gets
// that refusal as a value.
const script = `readme = read_file(path="README.md")
print("README:", readme["content"].splitlines()[1])
todos = search_files(pattern="TODO")
print("TODOs:", len(todos["matches"]))
listing = execute(command="ls src")
print("src:", ", ".join(listing["output"].split()))
removed = execute(command="rm -rf src")
if is_error(removed):
    print("rm:", removed.error)
`

func run(ctx context.Context, w io.Writer) error {
	// 1. Create a sandbox. local refuses a Spec it cannot honour, and the
	// zero Network means "none", which it cannot: say what you accept.
	sb, err := local.Unisolated().Create(ctx, sandbox.Spec{Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted}})
	if err != nil {
		return err
	}
	defer sb.Close(ctx)
	fmt.Fprintf(w, "sandbox %s: agent %s, protocol %d.%d\n", sb.ID(), sb.Agent().Version, sb.Agent().ProtocolMajor, sb.Agent().ProtocolMinor)

	// 2. Put the project in, as a tar stream.
	res, err := sb.Sync(ctx, sandbox.Tar{R: bytes.NewReader(tarOf(project))})
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "synced %d files, %d bytes\n", res.Files, res.Bytes)

	// 3. Get the proxy tools: the same definitions as tools.All, run remotely.
	proxies, err := sb.Tools(ctx, sandbox.ToolOptions{})
	if err != nil {
		return err
	}
	byName := map[string]core.Tool{}
	for _, t := range proxies {
		byName[t.Name] = t
	}
	r := byName["read_file"].Execute(ctx, json.RawMessage(`{"path":"src/main.go"}`))
	fmt.Fprintf(w, "read_file src/main.go in the sandbox: ok=%v, %d bytes\n", r.OK, len(fmt.Sprint(r.Data["content"])))

	// 4. Drive them as any tools: here through code mode, under a guard.
	cm, _, err := codemode.New([]core.Tool{byName["read_file"], byName["search_files"], byName["execute"]}, codemode.Options{})
	if err != nil {
		return err
	}
	args, _ := json.Marshal(map[string]string{"script": script})
	agent, err := agentkit.New(agentkit.Config{
		Provider: faux.New(
			faux.Turn{Blocks: []core.ContentBlock{faux.FauxToolCall("call_1", cm.Name, string(args))}, StopReason: core.StopReasonToolUse},
			faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("Two TODOs, both in src/.")}, StopReason: core.StopReasonStop},
		),
		Model:    faux.Model().ID,
		Tools:    []core.Tool{cm},
		Guard:    guard.Restricted(guard.Options{AllowedPrograms: []string{"ls"}}),
		MaxTurns: 4,
	})
	if err != nil {
		return err
	}
	run, err := agent.Run(ctx, "How many TODOs are in the project?")
	if err != nil {
		return err
	}
	for _, m := range run.Messages {
		if tr, ok := m.(core.ToolResultMessage); ok {
			fmt.Fprintf(w, "\n%s result:\n  %s\n", tr.ToolName, strings.ReplaceAll(strings.TrimSpace(tr.Content.Text()), "\n", "\n  "))
		}
	}
	fmt.Fprintf(w, "\nmodel: %s\n", run.FinalText())

	// 5. Take files out again: Export validates every entry before W sees it.
	var out bytes.Buffer
	if err := sb.Export(ctx, "src", sandbox.TarOut{W: &out}); err != nil {
		return err
	}
	tr := tar.NewReader(&out)
	var names []string
	for h, err := tr.Next(); err == nil; h, err = tr.Next() {
		names = append(names, h.Name)
	}
	fmt.Fprintf(w, "exported src: %s\n\nSandbox example completed successfully\n", strings.Join(names, ", "))
	return nil
}

// tarOf builds a tar stream of files; writes to a bytes.Buffer do not fail.
func tarOf(files [][2]string) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range files {
		_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: f[0], Mode: 0o644, Size: int64(len(f[1]))})
		_, _ = tw.Write([]byte(f[1]))
	}
	_ = tw.Close()
	return buf.Bytes()
}
