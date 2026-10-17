// Command harness shows a coding harness — Claude Code, Pi, or any other MCP
// client — implementing one task of a specification package inside an
// AgentKit sandbox, with code mode and the sandbox's built-in tools as its
// only tools.
//
//	cd examples/harness && go run .
//
// This program is the embedder. It owns the sandbox and the policy, and the
// harness owns the model. It creates a sandbox and fills the workspace from
// a git repository; runs specr, the spec package renderer from
// github.com/agent-fox-dev/spec, inside the sandbox over the proxy execute
// tool; measures the spec's own checks on the unchanged tree as the baseline;
// and writes the prompt an af-impl run works from. Then it serves code_mode
// and the proxy tools to the harness over MCP on a loopback port, under the
// shell guard, starts the harness with its own tools turned off and this
// server as its only tool source, and when the harness exits measures the
// checks again, reports the verdict and exports the workspace to the host.
//
// It runs offline, with no API key, no container engine and no harness
// installed: the default run is on local with a sample project and a
// stand-in specr, and the harness is a scripted stand-in — AgentKit's own
// driver, importing the server's tools through AgentKit's MCP client as any
// embedder would, with provider/faux replaying one code_mode call that
// implements the task. local is NOT isolation. The real run is:
//
//	podman build -t agentkit-harness:latest -f Containerfile .
//	go run . --podman --image agentkit-harness:latest --repo https://github.com/you/project --spec 16 --harness claude
//
// See README.md for the image, the flags and what each harness is told.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/codemode"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/guard"
	"github.com/agent-fox-dev/agentkit-go/mcpserve"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
	"github.com/agent-fox-dev/agentkit-go/sandbox/podman"
)

// options are the run's flags.
type options struct {
	Podman  bool   // run on the podman provider instead of local
	Image   string // the sandbox image for --podman
	Network string // the sandbox's network for --podman: none or unrestricted
	Repo    string // a public https repository to clone into the sandbox
	Ref     string // its branch or tag
	Dir     string // a host checkout to sync instead of --repo
	Spec    string // the package, as specr takes it; empty means the sample's
	Task    int    // the task; 0 means the next one not done
	Harness string // claude, pi or scripted
	Model   string // the harness's model; empty means its default
	Allow   string // the programs the shell may run, comma-separated
	Out     string // the host directory the workspace is exported to
	// MaxTurns bounds the harness; SpecTokens bounds specr's render.
	MaxTurns, SpecTokens int
}

func main() {
	var o options
	flag.BoolVar(&o.Podman, "podman", false, "run on the podman provider (a container) instead of local, which is not isolation")
	flag.StringVar(&o.Image, "image", "", "the sandbox image for --podman: one built from Containerfile; empty means sandbox.DefaultImage(), which has no go and no specr")
	flag.StringVar(&o.Network, "network", "none", "the sandbox's network for --podman: none or unrestricted (go needs it to download modules)")
	flag.StringVar(&o.Repo, "repo", "", "a public https git repository the sandbox clones (needs --podman); empty means the sample project")
	flag.StringVar(&o.Ref, "ref", "", "the branch or tag of --repo; empty means its default branch")
	flag.StringVar(&o.Dir, "dir", "", "a host checkout to sync into the sandbox instead of --repo (a private repository is cloned here first)")
	flag.StringVar(&o.Spec, "spec", "", "the spec package, as specr takes it: a path, a number or a name; empty means the sample's")
	flag.IntVar(&o.Task, "task", 0, "the task to implement; 0 means the next task not done")
	flag.StringVar(&o.Harness, "harness", "scripted", "the harness: claude (Claude Code), pi, or scripted (AgentKit's driver over MCP with a scripted model)")
	flag.StringVar(&o.Model, "model", "", "the model claude or pi should use; empty means their default")
	flag.StringVar(&o.Allow, "allow", "go,gofmt,git,specr", "the programs the sandbox's shell may run")
	flag.StringVar(&o.Out, "out", "", "the host directory the workspace is exported to; empty means a temporary one")
	flag.IntVar(&o.MaxTurns, "max-turns", 40, "the harness's turn bound")
	flag.IntVar(&o.SpecTokens, "spec-tokens", 10000, "specr --max-tokens: the render must fit the shell tool's 50 KB of output")
	flag.Parse()
	if err := run(context.Background(), os.Stdout, os.Stderr, o); err != nil {
		fmt.Fprintln(os.Stderr, "harness:", err)
		os.Exit(1)
	}
}

// run is the whole flow. w gets the account of the run; log gets every tool
// call as it happens and the harness's own stderr.
func run(ctx context.Context, w, log io.Writer, o options) error {
	tmp, err := os.MkdirTemp("", "agentkit-harness-example-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	allowed := strings.Split(o.Allow, ",")
	for i := range allowed {
		allowed[i] = strings.TrimSpace(allowed[i])
	}

	// 1. The provider and the spec. The sample run is on local, whose only
	//    network mode is unrestricted and whose tools run with this
	//    process's privileges; it gets the stand-in specr on its PATH and
	//    this host's Go caches, so `go test` inside it is warm. A podman
	//    sandbox gets the zero network unless asked otherwise, bounded
	//    memory and PIDs, and a Go environment under /tmp, the only writable
	//    place besides the workspace in a read-only container.
	var p sandbox.Provider
	spec := sandbox.Spec{TTL: time.Hour, Labels: map[string]string{"example": "harness"}}
	sample := o.Repo == "" && o.Dir == ""
	if o.Podman {
		if p, err = podman.New(podman.Options{}); err != nil {
			return err
		}
		spec.Image = o.Image
		spec.Resources = sandbox.Resources{MemoryBytes: 4 << 30, PIDs: 1024}
		spec.Env = map[string]string{
			"PATH":        "/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
			"GOPATH":      "/tmp/go",
			"GOCACHE":     "/tmp/go-build",
			"GOMODCACHE":  "/tmp/go/pkg/mod",
			"GOTOOLCHAIN": "local",
			"GOFLAGS":     "-buildvcs=false",
		}
		switch o.Network {
		case "none":
		case "unrestricted":
			spec.Network.Mode = sandbox.NetworkUnrestricted
		default:
			return fmt.Errorf("--network %q: none or unrestricted", o.Network)
		}
		if sample {
			return errors.New("--podman needs --repo or --dir: the sample's stand-in specr runs only on local")
		}
	} else {
		p = local.Unisolated()
		spec.Network.Mode = sandbox.NetworkUnrestricted
		spec.Env = map[string]string{}
		if sample {
			bin := filepath.Join(tmp, "bin")
			if err := writeStandInSpecr(bin); err != nil {
				return err
			}
			spec.Env["PATH"] = bin + string(os.PathListSeparator) + os.Getenv("PATH")
		}
		for _, k := range []string{"GOCACHE", "GOMODCACHE", "GOPATH", "GOTOOLCHAIN"} {
			if v := goEnv(k); v != "" {
				spec.Env[k] = v
			}
		}
		if o.Repo != "" {
			return errors.New("--repo needs --podman: local has no init step to clone with; clone it on the host and pass --dir")
		}
	}

	// 2. Choose where the workspace comes from, then create the sandbox, fill
	//    it and get its proxy tools with one sandbox.Open. A GitRepo is the
	//    provider's init step: a clone inside the sandbox, the host never
	//    holding the tree. A LocalDir moves a host directory in over rsync,
	//    which is how a private repository, cloned on the host with the host's
	//    credential, goes in without the credential: ExcludeGit leaves .git
	//    behind.
	var (
		src    sandbox.Source
		filled string
	)
	switch {
	case o.Repo != "":
		src, filled = sandbox.GitRepo{URL: o.Repo, Ref: o.Ref, Depth: 1}, o.Repo
	case o.Dir != "":
		src, filled = sandbox.LocalDir{Path: o.Dir, ExcludeGit: true}, o.Dir
	default:
		project := filepath.Join(tmp, "project")
		if err := writeSample(project); err != nil {
			return err
		}
		src, filled = sandbox.LocalDir{Path: project}, "the sample project"
	}
	sb, proxies, err := sandbox.Open(ctx, p, spec, src, sandbox.ToolOptions{})
	if err != nil {
		return err
	}
	defer sb.Close(ctx)
	caps := p.Capabilities()
	fmt.Fprintf(w, "sandbox %s on %s (%s): agent %s, network %s\n", sb.ID(), caps.Provider, isolation(caps.Isolation), sb.Agent().Version, sb.Spec().Network.Mode)
	fmt.Fprintf(w, "filled the workspace from %s\n", filled)

	// 3. The runner that runs the proxy tools under the guard. guard.Allow is
	//    an allowlist of programs with shell operators refused; under a podman
	//    sandbox with no network, guard.AllowAll is a reasonable alternative,
	//    because then the container is the boundary. local has no boundary but
	//    this guard.
	runner, hk, info, err := buildRunner(proxies, guard.Allow(allowed...), log)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "serving code_mode over %d proxy tools (its description is %d bytes) and the %d tools themselves\n", info.BoundToolsCount, info.DescriptionBytes, len(proxies))

	// 4. specr, inside the sandbox, through the proxy execute tool: the
	//    package scoped to the task, as af-impl's "one task at a time" step
	//    reads it. The call goes through the guard like the harness's will.
	specRef := o.Spec
	task := o.Task
	if sample {
		specRef, task = sampleSpec, sampleTask
	}
	render, err := renderSpec(ctx, runner, specRef, task, o.SpecTokens)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "%s\n", render.Summary)
	if render.Result.Task == nil {
		return fmt.Errorf("specr rendered %s without a task scope", render.Result.SpecDir)
	}

	// 5. The baseline: the spec's own checks on the unchanged tree. A red
	//    baseline is a fact the prompt carries, not a blocker.
	tc, err := readTestCommands(ctx, runner, render.Result.SpecDir)
	if err != nil {
		return err
	}
	baseline := runChecks(ctx, runner, tc)
	fmt.Fprintln(w, "baseline:")
	for _, c := range baseline {
		fmt.Fprintf(w, "  %s\n", c)
	}

	// 6. Serve the tools to the harness over MCP, on loopback, behind a token
	//    only this process and the harness it starts know.
	srv, err := mcpserve.New(runner, mcpserve.Options{
		Instructions: "The tools run in a sandbox that holds the workspace. code_mode runs a Starlark script over the other tools in one call.",
	})
	if err != nil {
		return err
	}
	ep, err := srv.ServeLoopback(ctx)
	if err != nil {
		return err
	}
	defer ep.Close()
	fmt.Fprintf(w, "MCP server at %s\n", ep.URL)

	// 7. The harness, with the rules as its appended system prompt and the
	//    task as its prompt. It holds the model credential; the sandbox and
	//    this server never see it.
	system := systemPrompt(runner.Tools(), allowed)
	prompt := taskPrompt(render, baseline)
	fmt.Fprintf(w, "starting harness %s on task %d of %s (%s): prompt %d bytes, system prompt %d bytes\n",
		o.Harness, render.Result.Task.ID, render.Result.SpecDir, render.Result.Task.Title, len(prompt), len(system))
	hr, err := runHarness(ctx, log, o.Harness, ep, system, prompt, o)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "\nharness %s exited %d", hr.Harness, hr.ExitCode)
	if hr.Turns > 0 {
		fmt.Fprintf(w, " after %d turns", hr.Turns)
	}
	if hr.CostUSD > 0 {
		fmt.Fprintf(w, ", $%.4f", hr.CostUSD)
	}
	fmt.Fprintf(w, "; %d tool calls, %d blocked\n\nharness said:\n%s\n", hk.calls.Load(), hk.blocked.Load(), indent(strings.TrimSpace(hr.Final)))

	// 8. Measure, do not assert: the checks again, and af-impl's verdict.
	after := runChecks(ctx, runner, tc)
	text, landable := verdict(baseline, after)
	fmt.Fprintln(w, "\nafter the task:")
	for _, c := range after {
		fmt.Fprintf(w, "  %s\n", c)
	}
	fmt.Fprintf(w, "verdict: %s\n", text)
	if o.Repo != "" && sb.Agent().Git {
		if status := gitStatus(ctx, runner); status != "" {
			fmt.Fprintf(w, "git status --short:\n%s\n", indent(status))
		}
	}

	// 9. Export the workspace to the host, .git left behind. Landing the
	//     work — a branch, a commit, a pull request — is the embedder's,
	//     here, where the credential is.
	out := o.Out
	if out == "" {
		out = filepath.Join(tmp, "out")
	}
	if err := sb.Export(ctx, ".", sandbox.LocalDir{Path: out}); err != nil {
		return err
	}
	exported, _ := filepath.Glob(filepath.Join(out, "*"))
	for i := range exported {
		exported[i] = filepath.Base(exported[i])
	}
	fmt.Fprintf(w, "exported the workspace to %s: %s\n", out, strings.Join(exported, ", "))
	if !landable {
		fmt.Fprintln(w, "\nHarness example completed: the task did not land")
		return nil
	}
	fmt.Fprintln(w, "\nHarness example completed successfully")
	return nil
}

// gitStatus runs `git status --short` in the sandbox through the runner, under
// the guard like every other call, and returns what it printed. It is empty
// when the call did not run, failed or printed nothing.
func gitStatus(ctx context.Context, r *agentkit.Runner) string {
	res := r.Call(ctx, "execute", json.RawMessage(`{"command":"git status --short","timeout_s":60}`))
	if !res.OK || res.Data["outcome"] != "ok" {
		return ""
	}
	status, _ := res.Data["output"].(string)
	return strings.TrimSpace(status)
}

// buildRunner binds the proxies into one code_mode tool and builds the runner
// that runs it beside them. The runner is the whole pipeline: it prepares and
// checks each call's arguments, asks the guard, runs the handler with a nested
// caller attached for code_mode, and calls the toolset's hooks. The guard it
// asks is configured's, wrapped so the hooks can log and count what it
// decides; configured sees every call, direct or from a script.
func buildRunner(proxies []core.Tool, configured core.BeforeToolCall, log io.Writer) (*agentkit.Runner, *hooks, codemode.BuildInfo, error) {
	if configured == nil {
		return nil, nil, codemode.BuildInfo{}, errors.New("a guard is required: the proxies include a shell")
	}
	cm, info, err := codemode.New(proxies, codemode.Options{})
	if err != nil {
		return nil, nil, info, err
	}
	h := &hooks{w: log}
	r, err := agentkit.NewRunner(agentkit.Toolset{
		Tools:  append([]core.Tool{cm}, proxies...),
		Guard:  h.guard(configured),
		After:  h.after,
		Events: h.event,
	})
	if err != nil {
		return nil, nil, info, err
	}
	return r, h, info, nil
}

// goEnv reads one `go env` variable of this host, or "".
func goEnv(name string) string {
	out, err := exec.Command("go", "env", name).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func isolation(i sandbox.Isolation) string {
	switch i {
	case sandbox.IsolationNone:
		return "no isolation"
	case sandbox.IsolationConfinement:
		return "confinement"
	case sandbox.IsolationContainer:
		return "container"
	case sandbox.IsolationMicroVM:
		return "micro-VM"
	}
	return fmt.Sprint(i)
}
