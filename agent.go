// Package agentkit is a Go agent SDK.
//
// The loop, the tool system and the provider abstraction are ordinary Go you
// can read and step through. Nothing is hidden inside a subprocess or a graph
// engine.
//
// The canonical vocabulary — messages, content blocks, events, Tool and
// every interface seam — lives in the core package and is used directly.
// What lives here is the driver: Config, New, the Agent, the loop and the
// tool batch executor. The prompt assembler and the execute guard are each
// their own package beneath this one.
package agentkit

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"

	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/prompt"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
)

// Effort is how much thinking a model puts into a turn.
type Effort = core.Effort

// The efforts a Config can ask for.
const (
	EffortLow    = core.EffortLow
	EffortMedium = core.EffortMedium
	EffortHigh   = core.EffortHigh
	EffortXHigh  = core.EffortXHigh
	EffortMax    = core.EffortMax
)

// Config is everything an Agent is built from.
type Config struct {
	// Client is an Anthropic SDK client, built by anthropic.Resolve or by the
	// caller.
	Client *sdk.Client
	// Provider, when set, is used instead of Client: a test double or a
	// custom provider.
	Provider core.ProviderClient
	// Model is the catalog id; catalog.Lookup supplies its limits and prices.
	Model  string
	Effort Effort
	System string
	// Prefix is sent after the system prompt on every request, with a cache
	// breakpoint on its last block.
	Prefix []core.Message
	Tools  []core.Tool
	// Policy selects from Tools, and from what they reach through wrappers.
	Policy core.ToolPolicy
	// Guard authorizes every call. It is required when a shell tool is
	// reachable.
	Guard core.BeforeToolCall
	After core.AfterToolCall
	// MaxTurns, MaxCostUSD and Timeout bound a run; zero means unbounded.
	MaxTurns   int
	MaxCostUSD float64
	Timeout    time.Duration
	// Prune ages old tool results out of the request; the zero value is off.
	Prune PruneOptions
	// MaxTokens caps each response; zero means DefaultMaxTokens.
	MaxTokens int
}

// DefaultMaxTokens is the output bound sent when Config.MaxTokens is zero.
// It is still capped at the model's own output cap; a caller who wants the
// whole cap says so. Providers size rate-limit reservations from max_tokens
// at request start, so defaulting to a 128K cap costs 128K of
// output-per-minute budget per turn and removes the only bound on a runaway
// turn. 32K is generous for tool-call-shaped output.
const DefaultMaxTokens = 32768

// PruneOptions controls how old tool results leave the request.
type PruneOptions struct {
	// Threshold is the fraction of the model's context window the estimated
	// request must reach before pruning; zero or less disables it.
	Threshold float64
	// KeepTurns is how many recent turns keep their tool results whole.
	KeepTurns int
}

// Agent runs prompts against one model with one tool set. It has no global
// state: a second agent is a second value.
//
// Its surface is five methods. Run and Stream execute a prompt to a stop
// condition; Messages, Usage and ReachableTools read its state and are safe
// from any goroutine while a run is in flight. A Run or Stream that overlaps
// another fails with core.ErrBusy rather than queueing: a prompt queued
// behind a running one was written against a transcript the caller could
// see, and by the time it ran the transcript would have changed underneath
// it.
type Agent struct {
	cfg    Config
	model  core.Model
	client core.ProviderClient
	// tools is the resolved set, fixed at construction: it is part of the
	// cached prompt prefix, so it never changes under a run.
	tools []core.Tool
	// fixedTokens estimates what every request carries besides its
	// messages: the system prompt and the tool definitions.
	fixedTokens int64

	// mu guards everything below. It is never held while user code runs —
	// no tool handler, interceptor or provider — and never acquired while a
	// batch's finalize mutex is held.
	mu sync.Mutex
	// running is the run slot.
	running    bool
	transcript core.Messages
	// usage is the lifetime total; runUsage the current run's share, which
	// RunResult.Usage reports and the cost bound reads.
	usage    core.Usage
	runUsage core.Usage
	// elided maps a transcript index of an assistant message to the tokens
	// pruning took off the request that message answered.
	elided map[int]int64
}

// New builds an Agent from cfg. It refuses a config the agent could not run
// safely: no way to reach a model, no model, an invalid tool hierarchy, or a
// shell tool reachable with no Guard to authorize it — a returned error here,
// rather than an unrestricted shell discovered on the first run.
func New(cfg Config) (*Agent, error) {
	if cfg.Client == nil && cfg.Provider == nil {
		return nil, errors.New("agentkit: Config needs an Anthropic client or provider")
	}
	if cfg.Model == "" {
		return nil, errors.New("agentkit: Config.Model is empty; a model identifier is required")
	}
	for _, t := range cfg.Tools {
		if err := checkTool(t); err != nil {
			return nil, err
		}
	}
	resolved := cfg.Policy.Resolve(cfg.Tools)
	for _, t := range resolved {
		if err := checkTool(t); err != nil {
			return nil, err
		}
	}
	if err := checkShellGuard(cfg.Guard, resolved); err != nil {
		return nil, err
	}

	// Provider, when set, is the dispatch path; the client is only the
	// default for it.
	m, _ := catalog.Lookup(cfg.Model)
	client := cfg.Provider
	if client == nil {
		client = anthropic.Provider(m, anthropic.Options{Client: cfg.Client})
	}
	cfg.Tools = append([]core.Tool(nil), cfg.Tools...)
	cfg.Prefix = append([]core.Message(nil), cfg.Prefix...)
	a := &Agent{cfg: cfg, model: m, client: client, tools: resolved}
	fixed := len(prompt.Build(cfg.System, resolved))
	if b, err := json.Marshal(core.ToolWires(resolved)); err == nil {
		fixed += len(b)
	}
	a.fixedTokens = int64(fixed) / charsPerToken
	return a, nil
}

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

func newID(prefix string) string {
	var b [8]byte
	// rand.Read from crypto/rand cannot fail on any supported platform; since
	// Go 1.24 it panics rather than returning an error.
	_, _ = rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}

// Messages returns the agent's transcript: every message of every run, as
// recorded. It is a copy, safe to read while a run is in flight.
func (a *Agent) Messages() core.Messages {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.transcript.Clone()
}

// Usage returns cumulative usage for the agent's lifetime.
func (a *Agent) Usage() core.Usage {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.usage
}

// ReachableTools returns every tool a run of this agent can call: the tools
// Config.Policy resolved, and everything those reach through wrappers
// (07-REQ-2.2). A check that a run is read-only, say, has to look at this set
// rather than at the top-level tools.
func (a *Agent) ReachableTools() []core.Tool {
	return core.ReachableTools(a.tools)
}

// claimSlot claims the run slot, or fails with ErrBusy.
func (a *Agent) claimSlot() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running {
		return core.ErrBusy
	}
	a.running = true
	a.runUsage = core.Usage{}
	return nil
}

func (a *Agent) releaseSlot() {
	a.mu.Lock()
	a.running = false
	a.mu.Unlock()
}
