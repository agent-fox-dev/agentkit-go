// Command toolbox calls AgentKit's built-in tools by hand, with no model: the
// same tools an agent gets from tools.For, run through the same agentkit.Runner
// path an agent's calls take, so you can see what each one takes and returns
// and try your own arguments.
//
//	go run ./examples/toolbox                      # a tour of every tool
//	go run ./examples/toolbox list                 # the tools and their parameters
//	go run ./examples/toolbox --dir . call find_symbol name=Runner kind=type
//	go run ./examples/toolbox --dir . call find_references '{"name":"Runner.Call"}'
//	go run ./examples/toolbox --dir . repl         # type calls, one per line
//
// It needs no API key and no network. Flags go before the command.
//
// The shell tools, execute and run_command, are in the set, and a Runner
// refuses a set that reaches a shell tool with no Guard. This program passes
// guard.Allow(programs...): with no --allow every shell call comes back as
// blocked_by_policy, and --allow ls,git lets exactly those programs run.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/guard"
	"github.com/agent-fox-dev/agentkit-go/schema"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

func main() {
	err := run(context.Background(), os.Stdin, os.Stdout, os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "toolbox:", err)
		os.Exit(1)
	}
}

const usage = `usage: toolbox [flags] [command]

commands:
  tour                          call every built-in tool on a throwaway sample project (the default)
  list                          the tools and their parameters
  call NAME [JSON | KEY=VALUE]  call one tool in --dir and print its result
  repl                          read calls from standard input, one per line

flags:
`

func run(ctx context.Context, in io.Reader, w io.Writer, args []string) error {
	fs := flag.NewFlagSet("toolbox", flag.ContinueOnError)
	fs.SetOutput(w)
	fs.Usage = func() { fmt.Fprint(w, usage); fs.PrintDefaults() }
	dir := fs.String("dir", ".", "workspace root for list, call and repl; the file tools cannot reach outside it")
	allow := fs.String("allow", "", "comma-separated programs execute and run_command may start; none by default")
	data := fs.Bool("data", false, "also print each result's structured Data as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cmd, rest := "tour", fs.Args()
	if len(rest) > 0 {
		cmd, rest = rest[0], rest[1:]
	}
	programs := strings.FieldsFunc(*allow, func(r rune) bool { return r == ',' })

	if cmd == "tour" {
		if len(rest) > 0 || flagSet(fs, "dir") || flagSet(fs, "allow") {
			return errors.New("tour takes no arguments and works in a sample project of its own: it writes files")
		}
		return tour(ctx, w)
	}

	// The workspace is the containment boundary every file tool resolves its
	// paths against. Opening it reads nothing: ignore files are read when a
	// tool first walks the tree.
	ws, err := workspace.Open(*dir, workspace.Options{})
	if err != nil {
		return err
	}
	runner, err := newRunner(ws, programs)
	if err != nil {
		return err
	}
	switch cmd {
	case "list":
		return list(w, runner)
	case "call":
		if len(rest) == 0 {
			return errors.New("call needs a tool name; `list` shows them")
		}
		return call(ctx, w, runner, rest[0], rest[1:], *data)
	case "repl":
		return repl(ctx, in, w, runner, *data)
	}
	return fmt.Errorf("unknown command %q\n\n%s", cmd, usage)
}

// flagSet reports whether the flag called name was given on the command line.
func flagSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) { set = set || f.Name == name })
	return set
}

// newRunner builds the tools for ws and a Runner over them.
//
// tools.For is the default set: read, write, edit, list, find and search, the
// three navigation tools (file_outline, find_symbol, find_references) and the
// two shell tools. A Runner runs a call as an agent run does, minus the model:
// it looks the tool up, prepares and checks the arguments against the tool's
// schema, asks the Guard, runs the handler, and always answers with a result,
// never a Go error. Calling a tool's Execute directly skips all of that but
// the handler.
func newRunner(ws *workspace.Workspace, programs []string) (*agentkit.Runner, error) {
	built, err := tools.For(ws, tools.Options{})
	if err != nil {
		return nil, err
	}
	return agentkit.NewRunner(agentkit.Toolset{Tools: built, Guard: guard.Allow(programs...)})
}

// list prints every tool of the runner with its parameters.
func list(w io.Writer, r *agentkit.Runner) error {
	for _, t := range r.Tools() {
		fmt.Fprintf(w, "%s\n  %s\n", t.Name, strings.ReplaceAll(strings.TrimSpace(t.Description), "\n", "\n  "))
		if t.InputSchema == nil {
			continue
		}
		for _, name := range propertyNames(t.InputSchema) {
			p := t.InputSchema.Properties[name]
			need := "optional"
			if slices.Contains(t.InputSchema.Required, name) {
				need = "required"
			}
			fmt.Fprintf(w, "    %-14s %-8s %-8s %s\n", name, p.Type, need, p.Description)
		}
		fmt.Fprintln(w)
	}
	return nil
}

// propertyNames are the parameter names of s in declaration order.
func propertyNames(s *schema.Schema) []string {
	if len(s.PropertyOrder) > 0 {
		return s.PropertyOrder
	}
	names := make([]string, 0, len(s.Properties))
	for name := range s.Properties {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// call runs one tool. args is one JSON object, or KEY=VALUE pairs.
func call(ctx context.Context, w io.Writer, r *agentkit.Runner, name string, args []string, withData bool) error {
	input, err := inputFor(r, name, args)
	if err != nil {
		return err
	}
	res := r.Call(ctx, name, input)
	fmt.Fprintf(w, "$ %s %s\n", name, input)
	show(w, res, 0, withData)
	return nil
}

// inputFor is the JSON input of a call to name. A single argument that starts
// with { is the input as it is. Otherwise each argument is KEY=VALUE, and the
// value is read as the type the tool's schema gives that key, so max_results=5
// is a number and name=5 is still the string "5". An array or an object is
// JSON: argv='["ls","-l"]'.
func inputFor(r *agentkit.Runner, name string, args []string) (json.RawMessage, error) {
	if len(args) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if len(args) == 1 && strings.HasPrefix(strings.TrimSpace(args[0]), "{") {
		return json.RawMessage(args[0]), nil
	}
	t, ok := toolNamed(r, name)
	if !ok {
		// Without the tool's schema there is nothing to read the values by.
		return nil, fmt.Errorf("no tool %q; `list` shows them", name)
	}
	obj := make(map[string]json.RawMessage, len(args))
	for _, a := range args {
		key, val, ok := strings.Cut(a, "=")
		if !ok {
			return nil, fmt.Errorf("%q is neither a JSON object nor KEY=VALUE", a)
		}
		p := t.InputSchema.Properties[key]
		if p == nil {
			return nil, fmt.Errorf("%s has no parameter %q; it takes %s", name, key, strings.Join(propertyNames(t.InputSchema), ", "))
		}
		raw, err := encodeValue(p.Type, val)
		if err != nil {
			return nil, fmt.Errorf("%s=%s: %w", key, val, err)
		}
		obj[key] = raw
	}
	return json.Marshal(obj)
}

// encodeValue is val as the JSON value of a parameter of type typ.
func encodeValue(typ schema.Type, val string) (json.RawMessage, error) {
	switch typ {
	case schema.TypeInteger:
		n, err := strconv.Atoi(val)
		if err != nil {
			return nil, errors.New("want an integer")
		}
		return json.Marshal(n)
	case schema.TypeNumber:
		n, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return nil, errors.New("want a number")
		}
		return json.Marshal(n)
	case schema.TypeBoolean:
		b, err := strconv.ParseBool(val)
		if err != nil {
			return nil, errors.New("want true or false")
		}
		return json.Marshal(b)
	case schema.TypeArray, schema.TypeObject:
		if !json.Valid([]byte(val)) {
			return nil, errors.New("want JSON")
		}
		return json.RawMessage(val), nil
	}
	return json.Marshal(val)
}

func toolNamed(r *agentkit.Runner, name string) (core.Tool, bool) {
	for _, t := range r.Tools() {
		if t.Name == name {
			return t, true
		}
	}
	return core.Tool{}, false
}

// repl runs the calls typed on in until it ends or says quit. The runner, and
// with it each tool's caches (the symbol table find_symbol builds on first
// use, the packages find_references has type-checked), lives across lines, so
// a second call is faster than the first, and a write_file or edit_file is
// seen by the next lookup.
func repl(ctx context.Context, in io.Reader, w io.Writer, r *agentkit.Runner, withData bool) error {
	fmt.Fprintln(w, "NAME {json} or NAME key=value ...; list shows the tools; quit leaves")
	sc := bufio.NewScanner(in)
	sc.Buffer(nil, 1<<20)
	for {
		fmt.Fprint(w, "> ")
		if !sc.Scan() {
			fmt.Fprintln(w)
			return sc.Err()
		}
		name, rest, _ := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		rest = strings.TrimSpace(rest)
		var err error
		switch name {
		case "":
			continue
		case "quit", "exit":
			return nil
		case "list":
			err = list(w, r)
		default:
			args := strings.Fields(rest)
			if strings.HasPrefix(rest, "{") {
				args = []string{rest}
			}
			err = call(ctx, w, r, name, args, withData)
		}
		if err != nil {
			fmt.Fprintf(w, "  %v\n", err)
		}
	}
}

// show prints a result the way a person reads it: the text the model would be
// given, or the error code and detail. maxLines cuts a long text for the tour;
// zero prints it whole.
func show(w io.Writer, res core.ToolResult, maxLines int, withData bool) {
	if !res.OK {
		fmt.Fprintf(w, "  error %s: %s\n", res.Error, strings.ReplaceAll(strings.TrimSpace(res.Detail), "\n", "\n  "))
		return
	}
	lines := strings.Split(strings.TrimRight(res.LLMText(), "\n"), "\n")
	more := 0
	if maxLines > 0 && len(lines) > maxLines {
		lines, more = lines[:maxLines], len(lines)-maxLines
	}
	for _, l := range lines {
		fmt.Fprintf(w, "  %s\n", l)
	}
	if more > 0 {
		fmt.Fprintf(w, "  ... %d more lines\n", more)
	}
	if m := res.Metadata; m != nil && m.Truncated {
		fmt.Fprintf(w, "  [truncated by %s]\n", m.TruncatedBy)
	}
	if withData {
		b, err := json.MarshalIndent(res.Data, "  ", "  ")
		if err != nil {
			b = []byte(err.Error())
		}
		fmt.Fprintf(w, "  data: %s\n", b)
	}
}
