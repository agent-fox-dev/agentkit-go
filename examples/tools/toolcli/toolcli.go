// Package toolcli runs one agent tool from the command line, the way the agent
// loop runs it for a model.
//
// Every program under examples/tools is one line — toolcli.Main("<name>") —
// and this package is the plumbing they share. A call goes through the same
// steps it goes through inside an agent:
//
//  1. the arguments are parsed as the model's tool_use input
//     (core.NewToolUse);
//  2. they pass the argument pipeline — per-tool repair, optional-null
//     removal, coercion, validation (core.PrepareArguments);
//  3. they pass the authorization boundary (a core.BeforeToolCall; by default
//     guard.Restricted, as in examples/codingagent);
//  4. the tool's own Execute runs;
//  5. the result is rendered with core.ToolResult.LLMText, which is what the
//     loop puts in the tool_result text block the model reads.
//
// Stdout carries exactly that text block and nothing else, so it can be piped.
// Everything else — the error flag, image blocks, the -data dump — goes to
// stderr.
//
// The package is not internal because the code_search program lives in the
// nested examples/codesearch module, which could not import it otherwise.
package toolcli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/guard"
	"github.com/agentfox/agentkit-go/tools"
)

// Builder returns the tools an agent would register for a workspace, and a
// function that releases whatever they hold. The cleanup may be nil.
type Builder func(ws *tools.Workspace) ([]core.Tool, func(), error)

// DefaultPrograms is the shell allowlist guard.Restricted starts from — the
// same short, read-only list examples/codingagent uses. -allow adds to it and
// -allow-all replaces the guard with guard.AllowAll.
var DefaultPrograms = []string{"go", "git", "ls", "cat", "rg"}

// Exit statuses.
const (
	ExitOK    = 0 // the tool returned a result with OK set
	ExitError = 1 // the tool returned an error result (the model sees is_error)
	ExitUsage = 2 // no call was made: bad flags or arguments that are not a JSON object, or the workspace or tool set could not be built
)

// Main runs the named built-in tool against os.Args and exits.
func Main(name string) { MainWith(name, Builtins) }

// MainWith is Main with a different tool set, for a tool that is not built in.
func MainWith(name string, build Builder) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := Run(ctx, name, build, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// Builtins is the set an agent gets from tools.All, plus fetch_url. An agent
// has to opt in to fetch_url (tools.FetchTool through ToolPolicy.CustomTools);
// a program named fetch_url is that opt-in.
func Builtins(ws *tools.Workspace) ([]core.Tool, func(), error) {
	ts, err := tools.All(tools.Options{Workspace: ws})
	if err != nil {
		return nil, nil, err
	}
	return append(ts, tools.FetchTool(tools.FetchOptions{})), nil, nil
}

// Run is the whole program, with its environment passed in so a test can
// drive it. It returns the exit status.
func Run(ctx context.Context, name string, build Builder, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", ".", "workspace root; the file tools cannot reach outside it")
	showSchema := fs.Bool("schema", false, "print the tool as the model sees it and exit")
	showData := fs.Bool("data", false, "also print the structured result (data, metadata) as JSON on stderr")
	allowAll := fs.Bool("allow-all", false, "no authorization guard (guard.AllowAll) — an unrestricted shell")
	var allow []string
	fs.Func("allow", "program the guard admits besides "+strings.Join(DefaultPrograms, ", ")+" (repeatable, or comma-separated)",
		func(s string) error { allow = append(allow, strings.Split(s, ",")...); return nil })
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: %s [flags] ['<json arguments>']\n\n"+
			"Calls the %s tool the way an agent does for a model. The arguments are\n"+
			"the JSON object the model would send; without one they are read from stdin.\n\n", name, name)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() > 1 {
		fmt.Fprintln(stderr, "error: pass the JSON arguments as ONE argument (quote them)")
		return ExitUsage
	}

	ws, err := tools.NewWorkspace(*dir)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return ExitUsage
	}
	set, cleanup, err := build(ws)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return ExitUsage
	}
	if cleanup != nil {
		defer cleanup()
	}
	tool, ok := find(set, name)
	if !ok {
		fmt.Fprintf(stderr, "error: no tool named %q in this build\n", name)
		return ExitUsage
	}

	if *showSchema {
		if err := printSchema(stdout, tool); err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return ExitUsage
		}
		return ExitOK
	}

	raw, err := readArgs(fs.Args(), stdin)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return ExitUsage
	}

	before := guard.Restricted(guard.Options{AllowedPrograms: slices.Concat(DefaultPrograms, allow)})
	if *allowAll {
		before = guard.AllowAll
	}

	r, err := Call(ctx, tool, raw, before)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return ExitUsage
	}
	return report(r, *showData, stdout, stderr)
}

// Call makes one tool call the way the loop does — argument pipeline, then the
// authorization boundary, then Execute — and returns the result. A failure in
// the first two steps is a result, exactly as the model would get it; the only
// Go error is arguments that are not a JSON object, which no provider would
// hand the loop.
func Call(ctx context.Context, tool core.Tool, raw json.RawMessage, before core.BeforeToolCall) (core.ToolResult, error) {
	call, err := core.NewToolUse("toolcli_1", tool.Name, raw)
	if err != nil {
		return core.ToolResult{}, fmt.Errorf("arguments are not a JSON object: %w", err)
	}
	prepared, err := core.PrepareArguments(tool, call)
	if err != nil {
		return core.ErrResult("invalid_arguments", err.Error()), nil
	}
	if before != nil {
		dec := before(ctx, core.BeforeToolCallContext{
			ToolName: tool.Name, ToolUseID: call.ID, Tool: tool,
			Arguments: prepared.Args, RawInput: call.Input,
			Batch: []core.ToolUseBlock{call}, TurnCount: 1,
		})
		if dec.Block {
			reason := dec.Reason
			if reason == "" {
				reason = "blocked by policy"
			}
			return core.ErrResult(core.BlockErrorCode, reason), nil
		}
		if dec.Arguments != nil {
			if prepared, err = prepared.TryWithArgs(dec.Arguments); err != nil {
				return core.ErrResult("invalid_arguments", "BeforeToolCall returned arguments that are not JSON: "+err.Error()), nil
			}
		}
	}
	if tool.Execute == nil {
		return core.ToolResult{}, errors.New("tool has no Execute function")
	}
	return tool.Execute(ctx, prepared.Raw), nil
}

// report prints the model's text block on stdout and the rest on stderr.
func report(r core.ToolResult, showData bool, stdout, stderr io.Writer) int {
	text := r.LLMText()
	fmt.Fprint(stdout, text)
	if !strings.HasSuffix(text, "\n") {
		fmt.Fprintln(stdout)
	}
	for _, b := range r.Blocks {
		if img, ok := b.(core.ImageBlock); ok {
			fmt.Fprintf(stderr, "[+ image block: %s, %d bytes base64]\n", img.MimeType, len(img.Data))
		} else {
			fmt.Fprintf(stderr, "[+ %T block]\n", b)
		}
	}
	if showData {
		enc := json.NewEncoder(stderr)
		enc.SetIndent("", "  ")
		_ = enc.Encode(r) // ok, data, error, detail, metadata — the envelope plus what ToLLMMap strips
	}
	if !r.OK {
		fmt.Fprintf(stderr, "[is_error: %s]\n", r.Error)
		return ExitError
	}
	return ExitOK
}

// printSchema prints the four things the model is told about a tool: the name
// and description and input schema it receives in the request's tool list,
// and the guidelines the system prompt adds for it.
func printSchema(w io.Writer, t core.Tool) error {
	s, err := json.MarshalIndent(t.InputSchema, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "name: %s\n\ndescription:\n%s\n\ninput schema:\n%s\n", t.Name, t.Description, s)
	if len(t.PromptGuidelines) > 0 {
		fmt.Fprintf(w, "\nprompt guidelines:\n")
		for _, g := range t.PromptGuidelines {
			fmt.Fprintf(w, "- %s\n", g)
		}
	}
	return nil
}

// readArgs takes the one positional argument, else all of stdin. Empty input
// is an empty object, as core.NewToolUse treats it.
func readArgs(pos []string, stdin io.Reader) (json.RawMessage, error) {
	if len(pos) == 1 {
		return json.RawMessage(pos[0]), nil
	}
	b, err := io.ReadAll(stdin)
	if err != nil {
		return nil, fmt.Errorf("reading arguments from stdin: %w", err)
	}
	return b, nil
}

func find(set []core.Tool, name string) (core.Tool, bool) {
	for _, t := range set {
		if t.Name == name {
			return t, true
		}
	}
	return core.Tool{}, false
}
