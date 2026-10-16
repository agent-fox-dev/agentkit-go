package agentkit

import (
	"fmt"
	"slices"
	"strings"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// Toolset is the tool side of an agent: the tools, the policy that selects
// among them, the guard that authorizes every call, the interceptor that sees
// every result, and the sink for execution events. It holds no model, so
// NewRunner builds from one what runs tool calls with no Agent, and
// Config.Toolset gives an Agent the same set. The zero Toolset has no tools,
// which is valid.
type Toolset struct {
	// Tools are the tools registered; Policy selects from them.
	Tools []core.Tool
	// Policy selects from Tools, and from what they reach through wrappers.
	Policy core.ToolPolicy
	// Guard authorizes every call. It is required when a shell tool is
	// reachable.
	Guard core.BeforeToolCall
	// After sees every finished call, and may vote to end an agent run.
	After core.AfterToolCall
	// Events receives the execution events of the calls a Runner runs outside
	// an agent run; a nil Events discards them. It may be called from several
	// goroutines at once, so it must be safe for concurrent use, and it should
	// not block. (*core.EventStream).Push is a conforming sink. During an
	// agent run the run's stream receives the events of the run's calls and
	// Events is not called for them.
	Events func(core.Event)
}

// Runner holds a validated Toolset. It is immutable after NewRunner returns,
// and safe for concurrent use by any number of goroutines. Only a *Runner from
// NewRunner or Agent.Runner is supported: the zero Runner has no validated
// tool set to stand on.
type Runner struct {
	guard core.BeforeToolCall
	after core.AfterToolCall
	// tools is the resolved set, in policy order. It is part of the cached
	// prompt prefix of an agent built on the runner, so it never changes.
	tools []core.Tool
}

// NewRunner validates ts and returns a Runner over it. It refuses a toolset
// the agent could not run safely: a tool with neither or both of Handler and
// Execute, an invalid tool hierarchy, or a shell tool reachable with no Guard
// to authorize it. The checks run in this order — every registered tool, the
// policy's resolution, every resolved tool, then the shell guard — and the
// first failure is returned with a nil Runner. New makes the same checks, by
// calling NewRunner, so the two refuse exactly the same toolsets with the same
// errors.
//
// NewRunner copies ts.Tools, so a caller who changes its own slice afterwards
// does not change the runner.
func NewRunner(ts Toolset) (*Runner, error) {
	tools := slices.Clone(ts.Tools)
	for _, t := range tools {
		if err := checkTool(t); err != nil {
			return nil, err
		}
	}
	resolved := ts.Policy.Resolve(tools)
	for _, t := range resolved {
		if err := checkTool(t); err != nil {
			return nil, err
		}
	}
	if err := checkShellGuard(ts.Guard, resolved); err != nil {
		return nil, err
	}
	return &Runner{guard: ts.Guard, after: ts.After, tools: resolved}, nil
}

// Tools returns the resolved tool set in policy order: what a model, an MCP
// server or a prompt should see. Each call returns a fresh slice.
func (r *Runner) Tools() []core.Tool { return slices.Clone(r.tools) }

// Reachable returns every tool the runner can call: the tools the policy
// resolved, and everything those reach through wrappers (07-REQ-2.2). A check
// that a toolset is read-only, say, has to look at this set rather than at
// the top-level tools.
func (r *Runner) Reachable() []core.Tool { return core.ReachableTools(r.tools) }

// checkTool is REQ-TOOL-01's "exactly one of Handler and Execute", for every
// way a tool enters the registry. A tool with neither otherwise registers and
// fails only when the model first calls it, as a nil-func panic.
//
// It also validates the tool's ReachableTools hierarchy (07-REQ-1): every
// reachable tool passes the same handler check, no tool reaches itself
// directly or transitively, and no wrapper reaches a Terminating tool.
// Failing here, at construction or registration, keeps a cycle from becoming
// unbounded recursion and a wrapper from ending the run mid-run.
func checkTool(t core.Tool) error {
	return checkToolTree(t, nil, map[string]bool{})
}

// checkToolTree checks t and what it reaches. path is the chain of names
// that led to t; a name already on it is a cycle. Tools are values, so a
// cycle can only exist through shared slice storage, and it is detected by
// name — the same name a nested call is dispatched by. done holds names
// whose subtree has been checked: a diamond reaches the same tool along two
// paths, and checking it once keeps a wide hierarchy from being walked once
// per path.
func checkToolTree(t core.Tool, path []string, done map[string]bool) error {
	for i, name := range path {
		if name == t.Name {
			cycle := append(append([]string(nil), path[i:]...), t.Name)
			return fmt.Errorf("agentkit: reachable tools cycle detected: %s", strings.Join(cycle, " -> "))
		}
	}
	if done[t.Name] {
		return nil
	}
	if t.Handler == nil && t.Execute == nil {
		return fmt.Errorf("agentkit: tool %q has neither Handler nor Execute", t.Name)
	}
	if t.Handler != nil && t.Execute != nil {
		return fmt.Errorf("agentkit: tool %q sets both Handler and Execute; exactly one", t.Name)
	}
	path = append(path[:len(path):len(path)], t.Name)
	for _, r := range t.ReachableTools {
		if r.Terminating {
			return fmt.Errorf("agentkit: terminating tool %q cannot be reached through wrapper %q", r.Name, t.Name)
		}
		if err := checkToolTree(r, path, done); err != nil {
			return err
		}
	}
	done[t.Name] = true
	return nil
}
