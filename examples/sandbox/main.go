// Command sandbox shows AgentKit's built-in tools running in a sandbox: an
// ephemeral environment that holds one workspace. Sandbox.Tools returns a
// drop-in replacement for tools.All whose every call runs in the AgentKit
// agent inside the sandbox; the driver does not change.
//
//	cd examples/sandbox && go run .
//
// The project goes in from a host directory with sandbox.LocalDir, and the
// result comes back out into another, over the rsync protocol.
//
// It runs offline, with no API key: the "model" is provider/faux replaying
// one scripted code_mode call, and the sandbox comes from local, the
// in-process provider. local is NOT isolation — it runs the agent in this
// process with its privileges, and exists for tests and development — but
// it speaks the same protocol, over the same transport, as a real provider.
//
// With --podman the same program runs on sandbox/podman, a hardened
// container with no network, from the base image --image names (build one
// with make sandbox-images):
//
//	go run . --podman --image quay.io/agent-fox-dev/agentkit-sandbox:latest
//
// With --landlock it runs on sandbox/landlock, the agent as a child process
// confined by its own Landlock ruleset under NetworkNone; --agent names the
// agentkit-sandboxd to run (install one with go install). It needs Linux
// with Landlock ABI 4; elsewhere it fails with landlock.ErrNoLandlock:
//
//	go run . --landlock --agent "$(go env GOPATH)/bin/agentkit-sandboxd"
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/codemode"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/guard"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/landlock"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
	"github.com/agent-fox-dev/agentkit-go/sandbox/podman"
)

func main() {
	usePodman := flag.Bool("podman", false, "run on the podman provider (a container with no network) instead of local")
	image := flag.String("image", "", "the sandbox image for --podman; empty means sandbox.DefaultImage()")
	useLandlock := flag.Bool("landlock", false, "run on the landlock provider (a confined child process, Linux only) instead of local")
	agentPath := flag.String("agent", "", "the agentkit-sandboxd for --landlock; empty means beside this program, then on PATH")
	flag.Parse()
	dir, err := os.MkdirTemp("", "agentkit-sandbox-example-")
	if err == nil {
		switch {
		case *usePodman:
			err = runPodman(context.Background(), os.Stdout, dir, *image)
		case *useLandlock:
			err = runLandlock(context.Background(), os.Stdout, dir, *agentPath)
		default:
			err = run(context.Background(), os.Stdout, dir)
		}
		_ = os.RemoveAll(dir)
	}
	if err != nil {
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

// run works in dir on local: the project is written to dir/project and the
// result exported to dir/out. local refuses a Spec it cannot honour, and the
// zero Network means "none", which it cannot: say what you accept.
func run(ctx context.Context, w io.Writer, dir string) error {
	return runOn(ctx, w, dir, local.Unisolated(), sandbox.Spec{Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted}})
}

// runPodman is run on the podman provider, under NetworkNone (the zero
// Network) with image, and bounded resources.
func runPodman(ctx context.Context, w io.Writer, dir, image string) error {
	p, err := podman.New(podman.Options{})
	if err != nil {
		return err
	}
	return runOn(ctx, w, dir, p, sandbox.Spec{Image: image, Resources: sandbox.Resources{MemoryBytes: 1 << 30, PIDs: 512}})
}

// runLandlock is run on the landlock provider, under NetworkNone (the zero
// Spec), with the agent binary agent.
func runLandlock(ctx context.Context, w io.Writer, dir, agent string) error {
	p, err := landlock.New(landlock.Options{AgentBinary: agent})
	if err != nil {
		return err
	}
	return runOn(ctx, w, dir, p, sandbox.Spec{})
}

// runOn works in dir on a sandbox p creates from spec.
func runOn(ctx context.Context, w io.Writer, dir string, p sandbox.Provider, spec sandbox.Spec) error {
	// 1. Create a sandbox.
	sb, err := p.Create(ctx, spec)
	if err != nil {
		return err
	}
	defer sb.Close(ctx)
	fmt.Fprintf(w, "sandbox %s on %s: agent %s, protocol %d.%d\n", sb.ID(), p.Capabilities().Provider, sb.Agent().Version, sb.Agent().ProtocolMajor, sb.Agent().ProtocolMinor)

	// 2. Put the project in from a host directory. Only what changed moves,
	// and the same ignore rules as the file tools apply.
	src := filepath.Join(dir, "project")
	for _, f := range project {
		p := filepath.Join(src, filepath.FromSlash(f[0]))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(f[1]), 0o644); err != nil {
			return err
		}
	}
	res, err := sb.Sync(ctx, sandbox.LocalDir{Path: src})
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

	// 5. Take files out again, into a host directory Export creates.
	out := filepath.Join(dir, "out")
	if err := sb.Export(ctx, "src", sandbox.LocalDir{Path: out}); err != nil {
		return err
	}
	ents, err := os.ReadDir(out)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	fmt.Fprintf(w, "exported src: %s\n\nSandbox example completed successfully\n", strings.Join(names, ", "))
	return nil
}
