package agentkit

import "github.com/agent-fox-dev/agentkit-go/runner"

// Toolset is the tool side of an agent: the tools, the policy that selects
// among them, the guard that authorizes every call, the interceptor that sees
// every result, and the sink for execution events. It holds no model, so
// NewRunner builds from one what runs tool calls with no Agent, and
// Config.Toolset gives an Agent the same set. The zero Toolset has no tools,
// which is valid.
//
// It is runner.Toolset: the two names are one type. A program that only runs
// tools imports package runner instead of this one, and so links no provider.
type Toolset = runner.Toolset

// Runner runs tool calls with no model: Call runs one and Batch runs several,
// each through the steps a model's calls go through in an agent run. It is
// runner.Runner, where its methods are documented: the two names are one type,
// so a *Runner from NewRunner, from runner.New or from Agent.Runner is
// accepted wherever either is asked for.
type Runner = runner.Runner

// NewRunner validates ts and returns a Runner over it. It is runner.New: it
// refuses what New refuses, with the same errors, and copies ts.Tools.
func NewRunner(ts Toolset) (*Runner, error) { return runner.New(ts) }
