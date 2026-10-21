// Package agentkit is a Go agent SDK.
//
// The loop, the tool system and the provider abstraction are ordinary Go you
// can read and step through. Nothing is hidden inside a subprocess or a graph
// engine.
//
// The canonical vocabulary — messages, content blocks, events, Tool and
// every interface seam — lives in the core package and is used directly.
// What lives here is the driver — Config, New, the Agent and the loop — and
// the tool side the driver is built on, which stands without a model. A
// Toolset is the tools, the policy that selects among them, the guard, the
// after-interceptor and the sink for execution events; NewRunner validates one
// into a Runner, which runs tool calls (Call, Batch), code mode included,
// through the pipeline an agent run uses. An Agent holds a Runner, and Config
// takes a Toolset. The prompt assembler and the execute guard are each their
// own package beneath this one.
package agentkit

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/prompt"
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
//
// Provider, Model and Effort choose what runs the agent, and Env and Getenv
// supply the credentials and endpoints of the provider they choose. Resolve
// shows what a Config resolves to; New builds an agent on exactly that.
type Config struct {
	// Client is the provider client the agent calls: a test double such as
	// provider/faux, or a provider the embedder built or wrapped. When it is
	// set the agent builds none and reads no variable. Provider and Model
	// still choose the catalog row and the effort.
	Client core.ProviderClient
	// Provider names the provider that serves Model: one of catalog.Providers,
	// matched without regard to case and trimmed. Empty means the first. It
	// names the catalog provider even when Client is set.
	Provider string
	// Model is a tier name (SIMPLE, STANDARD or ADVANCED) or the provider's own
	// model id; catalog.Select supplies its limits, prices and, for a tier, its
	// effort.
	Model string
	// Effort is one of the five efforts or empty. Empty takes the tier's effort
	// when Model is a tier, and sends none otherwise.
	Effort Effort
	// Env and Getenv supply the variables the chosen provider reads for its
	// credentials and endpoint. A non-empty Env value is used first; an empty
	// one falls through to Getenv, and with no Getenv to the process
	// environment. An empty value counts as unset. Nothing else reads them.
	Env    map[string]string
	Getenv func(string) string
	System string
	// Prefix is sent after the system prompt on every request, with a cache
	// breakpoint on its last block.
	Prefix []core.Message
	// Toolset is the agent's tools, the policy that selects among them, the
	// guard that authorizes every call and the interceptor that sees every
	// result. The zero Toolset is an agent with no tools. New refuses a
	// Toolset that NewRunner refuses.
	Toolset Toolset
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
// Its surface is seven methods. Run and Stream execute a prompt to a stop
// condition; Messages, Usage and ReachableTools read its state, Runner
// returns the runner its tool calls go through, and Resolution reports the
// provider, model and effort it was built to run, all safe from any goroutine
// while a run is in flight. A Run or Stream that overlaps another fails with
// core.ErrBusy rather than queueing: a prompt queued behind a running one was
// written against a transcript the caller could see, and by the time it ran
// the transcript would have changed underneath it.
type Agent struct {
	cfg Config
	// res is what Resolve chose, fixed at construction. model and client are
	// its row and its client, which the loop reads.
	res    Resolution
	model  core.Model
	client core.ProviderClient
	// runner holds the resolved tool set, guard and interceptor, fixed at
	// construction: the tool set is part of the cached prompt prefix, so it
	// never changes under a run.
	runner *Runner
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

// New builds an Agent from cfg. It runs Resolve first, so what it runs is what
// Resolve reports, and then validates the Toolset with NewRunner. It refuses a
// config the agent could not run safely, and returns the first error in this
// order, unchanged: the provider, the model, the effort, the credentials, and a
// Toolset that NewRunner refuses — an invalid tool hierarchy, or a shell tool
// reachable with no Guard to authorize it. That is a returned error here,
// rather than a missing key or an unrestricted shell discovered on the first
// run, and no request has been sent.
func New(cfg Config) (*Agent, error) {
	res, err := Resolve(cfg)
	if err != nil {
		return nil, err
	}
	runner, err := NewRunner(cfg.Toolset)
	if err != nil {
		return nil, err
	}

	// The runner owns the tool side now; the agent keeps no second copy of it.
	cfg.Toolset = Toolset{}
	cfg.Prefix = append([]core.Message(nil), cfg.Prefix...)
	// The resolution holds what the selection fields chose, and the variables
	// were read by the client's constructor: the agent keeps no second copy.
	cfg.Client, cfg.Env, cfg.Getenv = nil, nil, nil
	a := &Agent{cfg: cfg, res: res, model: res.Model, client: res.Client, runner: runner}
	tools := runner.Tools()
	fixed := len(prompt.Build(cfg.System, tools))
	if b, err := json.Marshal(core.ToolWires(tools)); err == nil {
		fixed += len(b)
	}
	a.fixedTokens = int64(fixed) / charsPerToken
	return a, nil
}

// Resolution returns the provider, model and effort the agent was built with and
// the client it calls: what Resolve returns for the same Config. It is a copy,
// so changing its Model does not change the agent's own row.
func (a *Agent) Resolution() Resolution {
	r := a.res
	r.Model = cloneModel(r.Model)
	return r
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
// Toolset.Policy resolved, and everything those reach through wrappers
// (07-REQ-2.2). A check that a run is read-only, say, has to look at this set
// rather than at the top-level tools. It is Runner().Reachable().
func (a *Agent) ReachableTools() []core.Tool {
	return a.Runner().Reachable()
}

// Runner returns the agent's runner: the same pointer on every call, safe to
// use while a run is in flight. The agent's own tool calls go through it, and
// so can an embedder's. Calls an embedder makes through it are not part of any
// run: they do not enter the transcript, they are not counted in Usage, they do
// not claim the run slot, and their events go to Toolset.Events.
func (a *Agent) Runner() *Runner { return a.runner }

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
