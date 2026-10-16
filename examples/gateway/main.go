// Command gateway shows sandboxes that run behind a sandbox gateway: a
// gRPC service that holds the backend provider and lets an embedder create,
// use and destroy sandboxes with only a URL and a bearer token. The client,
// gateway.Dial, returns a sandbox.Provider whose sandboxes behave like local
// ones: the same proxy tools, state machine and errors.
//
//	cd examples/gateway && go run .
//
// By default the program plays both parts on this machine. It starts a
// gatewayd.Server in process on a loopback port, over local (the in-process
// provider, which is NOT isolation), with a fresh random token, and then
// dials it over TCP as any embedder would. It runs offline, with no API key:
// the "model" is provider/faux replaying one scripted code_mode call.
//
// With --url it starts no gateway and dials one already running, such as an
// agentkit-gateway on this machine over Podman or Docker; --token-file names
// the file holding its token and --image the sandbox image:
//
//	make sandbox-gateway build-containers   # from the repository root
//	build/bin/agentkit-gateway --token-file tokens --plaintext --listen 127.0.0.1:8443
//	cd examples/gateway && go run . --url http://127.0.0.1:8443 --token-file ../../tokens \
//		--image quay.io/agentfox/agentkit-sandbox:latest
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/codemode"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/guard"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
)

func main() {
	url := flag.String("url", "", "dial the gateway at this URL instead of starting one in process")
	tokenFile := flag.String("token-file", "", "the file holding the token for --url; its first token is used")
	image := flag.String("image", "", "the sandbox image for --url; empty means sandbox.DefaultImage()")
	flag.Parse()
	dir, err := os.MkdirTemp("", "agentkit-gateway-example-")
	if err == nil {
		if *url != "" {
			err = runRemote(context.Background(), os.Stdout, dir, *url, *tokenFile, *image)
		} else {
			err = run(context.Background(), os.Stdout, os.Stderr, dir)
		}
		_ = os.RemoveAll(dir)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "gateway:", err)
		os.Exit(1)
	}
}

// run starts a gateway in process on a loopback port, over local, and works
// in dir through it. The gateway logs one structured line per create and
// destroy to logw.
func run(ctx context.Context, w, logw io.Writer, dir string) error {
	// The gateway's side: a backend, the accepted tokens and the limits. A
	// token is at least 32 printable characters; this one is random and lives
	// only as long as the program.
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	token := hex.EncodeToString(raw)
	srv, err := gatewayd.New(gatewayd.Config{
		Backend: local.Unisolated(),
		Tokens:  []string{token},
		// The network ceiling is intersected with the backend's modes, and
		// local offers only unrestricted. The default ceiling, [none], would
		// leave no mode in common, and New would refuse.
		NetworkModes: []sandbox.NetworkMode{sandbox.NetworkUnrestricted},
		MaxSandboxes: 4,
		MaxTTL:       time.Hour,
		Logger:       slog.New(slog.NewTextHandler(logw, nil)),
	})
	if err != nil {
		return err
	}
	// Plaintext, which the client accepts only on loopback: the token never
	// leaves this machine. Port 0 picks a free one.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(l) }()
	// Shutdown destroys every sandbox the gateway still holds.
	defer func() {
		_ = srv.Shutdown(ctx)
		<-served
	}()
	fmt.Fprintf(w, "gateway serving on %s (plaintext, loopback), backend %s\n", l.Addr(), local.Unisolated().Capabilities().Provider)

	// The embedder's side: a URL and a token, nothing else.
	return runOn(ctx, w, dir, "http://"+l.Addr().String(), token,
		sandbox.Spec{Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted}, TTL: 10 * time.Minute, Labels: map[string]string{"example": "gateway"}})
}

// runRemote works in dir through the gateway at url, with the first token in
// tokenFile, on a sandbox from image under NetworkNone (the zero Network),
// agentkit-gateway's default ceiling.
func runRemote(ctx context.Context, w io.Writer, dir, url, tokenFile, image string) error {
	token, err := firstToken(tokenFile)
	if err != nil {
		return err
	}
	return runOn(ctx, w, dir, url, token,
		sandbox.Spec{Image: image, Resources: sandbox.Resources{MemoryBytes: 1 << 30, PIDs: 512}, TTL: 10 * time.Minute, Labels: map[string]string{"example": "gateway"}})
}

// firstToken is the first token in a token file in agentkit-gateway's
// format: one per line, blank lines and lines starting with # ignored.
func firstToken(path string) (string, error) {
	if path == "" {
		return "", errors.New("--url needs --token-file")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		if line := strings.TrimSpace(s.Text()); line != "" && !strings.HasPrefix(line, "#") {
			return line, nil
		}
	}
	if err := s.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("%s holds no token", path)
}

// project is what the embedder puts into the sandbox: name, then content.
var project = [][2]string{
	{"README.md", "# Greeter\nSays hello from behind the gateway.\n"},
	{"src/main.go", "package main\n\n// TODO: take the name from the command line.\nfunc main() { println(\"hello\") }\n"},
	{"src/util.go", "package main\n\n// TODO: remove.\nfunc unused() {}\n"},
}

// script is the model's code_mode call. Every tool call it makes is relayed
// by the gateway to the agent in the sandbox. Its last call is refused by
// the guard, on the embedder's side, before anything reaches the gateway.
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

// runOn dials the gateway at url with token and works in dir on a sandbox
// created there from spec.
func runOn(ctx context.Context, w io.Writer, dir, url, token string, spec sandbox.Spec) error {
	// 1. Dial. The URL and token are checked before any connection, and the
	// gateway's capabilities are fetched and cached.
	p, err := gateway.Dial(ctx, url, gateway.Options{Token: token})
	if err != nil {
		return err
	}
	defer p.Close() // destroys the sandboxes p created, then disconnects
	gi := p.Gateway()
	fmt.Fprintf(w, "dialled %s: gateway %s, protocol %d.%d, backend %s, max TTL %v, max %d sandboxes\n",
		gi.URL, gi.Version, gi.ProtocolMajor, gi.ProtocolMinor, gi.Backend, gi.MaxTTL, gi.MaxSandboxes)

	// A token the gateway does not hold is refused on the first RPC.
	if _, err := gateway.Dial(ctx, url, gateway.Options{Token: "not-a-token-this-gateway-accepts"}); errors.Is(err, gateway.ErrUnauthenticated) {
		fmt.Fprintln(w, "a wrong token: refused (gateway.ErrUnauthenticated)")
	} else {
		return fmt.Errorf("a wrong token was not refused as unauthenticated: %v", err)
	}

	// 2. Create a sandbox on the gateway's host. From here on it is a
	// sandbox.Sandbox like any other.
	sb, err := p.Create(ctx, spec)
	if err != nil {
		return err
	}
	defer sb.Close(ctx)
	fmt.Fprintf(w, "sandbox %s on %s: agent %s, protocol %d.%d\n", sb.ID(), p.Capabilities().Provider, sb.Agent().Version, sb.Agent().ProtocolMajor, sb.Agent().ProtocolMinor)
	infos, err := p.List(ctx)
	if err != nil {
		return err
	}
	for _, in := range infos {
		fmt.Fprintf(w, "listed %s, labels %v, expires in %v\n", in.ID, in.Labels, in.Expires.Sub(in.Created).Round(time.Second))
	}

	// 3. Put the project in from a host directory; the rsync stream is relayed
	// by the gateway.
	src := filepath.Join(dir, "project")
	for _, f := range project {
		path := filepath.Join(src, filepath.FromSlash(f[0]))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(f[1]), 0o644); err != nil {
			return err
		}
	}
	res, err := sb.Sync(ctx, sandbox.LocalDir{Path: src})
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "synced %d files, %d bytes\n", res.Files, res.Bytes)

	// 4. The proxy tools, driven through code mode under a guard. The guard
	// runs here, in the embedder; the gateway only relays what it allows.
	proxies, err := sb.Tools(ctx, sandbox.ToolOptions{})
	if err != nil {
		return err
	}
	byName := map[string]core.Tool{}
	for _, t := range proxies {
		byName[t.Name] = t
	}
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
		Toolset:  agentkit.Toolset{Tools: []core.Tool{cm}, Guard: guard.Restricted(guard.Options{AllowedPrograms: []string{"ls"}})},
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
	fmt.Fprintf(w, "exported src: %s\n", strings.Join(names, ", "))

	// 6. Closing the sandbox destroys it on the gateway.
	if err := sb.Close(ctx); err != nil {
		return err
	}
	if infos, err = p.List(ctx); err != nil {
		return err
	}
	fmt.Fprintf(w, "closed: the gateway holds %d sandboxes for this token\n\nGateway example completed successfully\n", len(infos))
	return nil
}
