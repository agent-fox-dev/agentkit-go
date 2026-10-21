package providerconformance

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testkit"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
	"github.com/agent-fox-dev/agentkit-go/provider/ollama"
	"github.com/agent-fox-dev/agentkit-go/provider/openrouter"
)

// providerInfo is what the suite knows about one provider besides how its
// vendor's wire looks: the error values its failures carry.
type providerInfo struct {
	name   string
	vendor vendor
	// truncated is the provider's ErrStreamTruncated.
	truncated error
	// statusCode says whether err is the provider's *StatusError, by errors.As,
	// and its Code.
	statusCode func(err error) (int, bool)
}

// providerTable lists the three providers in the catalog's order.
var providerTable = []providerInfo{
	{
		name: "anthropic", vendor: anthropicVendor, truncated: anthropic.ErrStreamTruncated,
		statusCode: func(err error) (int, bool) {
			var se *anthropic.StatusError
			if errors.As(err, &se) {
				return se.Code, true
			}
			return 0, false
		},
	},
	{
		name: "openrouter", vendor: openRouterVendor, truncated: openrouter.ErrStreamTruncated,
		statusCode: func(err error) (int, bool) {
			var se *openrouter.StatusError
			if errors.As(err, &se) {
				return se.Code, true
			}
			return 0, false
		},
	},
	{
		name: "ollama", vendor: ollamaVendor, truncated: ollama.ErrStreamTruncated,
		statusCode: func(err error) (int, bool) {
			var se *ollama.StatusError
			if errors.As(err, &se) {
				return se.Code, true
			}
			return 0, false
		},
	},
}

// providerByName returns the provider of the name, or panics: a name is a
// constant of the suite.
func providerByName(name string) providerInfo {
	for _, p := range providerTable {
		if p.name == name {
			return p
		}
	}
	panic("providerconformance: no provider " + name)
}

// recordingEnv is the Getenv an agent is built with: a fixed map holding only
// one provider's variables, remembering every name it was asked for. The
// process environment is never read.
type recordingEnv struct {
	vars map[string]string

	mu    sync.Mutex
	asked []string
}

// fakeEnv is the environment of provider p with every variable pointed at the
// fake served at url.
func fakeEnv(p providerInfo, url string) *recordingEnv {
	return &recordingEnv{vars: p.vendor.env(url)}
}

// Getenv returns the variable's value, or "" for any name the map does not hold.
func (e *recordingEnv) Getenv(name string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.asked = append(e.asked, name)
	return e.vars[name]
}

// Asked lists the names Getenv was called with, in order.
func (e *recordingEnv) Asked() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.asked)
}

// newAgent builds the agent every case runs: agentkit.New from the provider's
// name and the STANDARD tier, the fake's variables as the only environment, one
// counting tool named echo and four turns at most. It returns the Config it
// passed.
func newAgent(p providerInfo, env *recordingEnv, echoCalls *atomic.Int32) (*agentkit.Agent, agentkit.Config, error) {
	cfg := agentkit.Config{
		Provider: p.name,
		Model:    "STANDARD",
		Getenv:   env.Getenv,
		Toolset:  agentkit.Toolset{Tools: []core.Tool{testkit.EchoTool("echo", echoCalls)}},
		MaxTurns: 4,
	}
	agent, err := agentkit.New(cfg)
	return agent, cfg, err
}

// conformanceCase is one logical scenario, run against every provider.
type conformanceCase struct {
	name   string
	prompt string
	// turns is the script of the fake for provider p.
	turns func(p providerInfo) []logicalTurn
	// check makes the assertions that belong to the case alone; nil for none.
	check func(r *caseRun)
}

// caseTimeout is how long a subtest may take.
const caseTimeout = 30 * time.Second

// runOptions vary how runCase runs a case. The zero value is a case as the
// suite runs it.
type runOptions struct {
	// timeout bounds the run; zero means caseTimeout.
	timeout time.Duration
	// hang makes the fake hold every request without answering.
	hang bool
}

// caseRun is one case run on one provider.
type caseRun struct {
	tb   reporter
	name string // <provider>/<case>
	p    providerInfo
	c    conformanceCase
	// sel is catalog.Select(provider, "STANDARD", ""): where every expected
	// model id and effort comes from.
	sel    catalog.Selection
	script []logicalTurn
	fake   *fake
	env    *recordingEnv
	// cfg is the Config the agent was built with.
	cfg       agentkit.Config
	agent     *agentkit.Agent
	echoCalls *atomic.Int32

	events    []core.Event
	res       core.RunResult
	runErr    error
	requests  []recordedRequest
	summaries []requestSummary
}

// fail reports a failure of the run, under its name.
func (r *caseRun) fail(format string, args ...any) {
	r.tb.Helper()
	r.tb.Errorf("%s: %s", r.name, fmt.Sprintf(format, args...))
}

// runCase runs the case on the provider against a fresh fake, under a context
// that ends after o.timeout. A run that does not finish by then is cancelled and
// reported as a failure naming the provider and the case. After a run that
// finishes it applies the checks common to every case, then the case's own, and
// last compares the number of requests the fake recorded with the script's.
func runCase(tb reporter, p providerInfo, c conformanceCase, o runOptions) *caseRun {
	tb.Helper()
	if o.timeout == 0 {
		o.timeout = caseTimeout
	}
	r := &caseRun{tb: tb, name: p.name + "/" + c.name, p: p, c: c, script: c.turns(p), echoCalls: new(atomic.Int32)}

	sel, err := catalog.Select(p.name, "STANDARD", "")
	if err != nil {
		r.fail("catalog.Select: %v", err)
		return r
	}
	r.sel = sel

	r.fake = newFake(tb, p.vendor, r.script...)
	defer r.fake.Close()
	if o.hang {
		r.fake.Hang()
	}
	r.env = fakeEnv(p, r.fake.URL())
	if r.agent, r.cfg, err = newAgent(p, r.env, r.echoCalls); err != nil {
		r.fail("agentkit.New: %v", err)
		return r
	}

	ctx, cancel := context.WithTimeout(context.Background(), o.timeout)
	defer cancel()
	stream, err := r.agent.Stream(ctx, c.prompt)
	if err != nil {
		r.fail("Agent.Stream: %v", err)
		return r
	}
	for e := range stream.Events() {
		r.events = append(r.events, e)
	}
	r.res, r.runErr = stream.RunResult()
	if ctx.Err() != nil {
		r.fail("the run did not finish within %v and was cancelled", o.timeout)
		return r
	}

	r.requests = r.fake.Requests()
	r.checkCommon()
	if c.check != nil {
		c.check(r)
	}
	checkCount(tb, r.name, len(r.script), len(r.requests))
	return r
}

// checkCount fails the case when the fake recorded another number of requests
// than the case scripts: an unexpected retry or an extra turn.
func checkCount(tb reporter, name string, scripted, recorded int) {
	tb.Helper()
	if scripted != recorded {
		tb.Errorf("%s: the case scripts %d requests and the fake recorded %d", name, scripted, recorded)
	}
}

// checkCommon applies the checks every case shares: every request carries the
// selected model and effort, every turn that ended normally has the faux event
// skeleton and whole block-end payloads, and every turn that failed ends in a
// terminal ErrorEvent and a MessageEndEvent with StopReasonError.
func (r *caseRun) checkCommon() {
	r.tb.Helper()
	var err error
	if r.summaries, err = r.fake.Summaries(); err != nil {
		r.fail("%v", err)
		return
	}
	for i, s := range r.summaries {
		if s.Model != r.sel.Model.ID {
			r.fail("request %d names model %q, want the selected %q", i+1, s.Model, r.sel.Model.ID)
		}
		if s.Effort != string(r.sel.Effort) {
			r.fail("request %d carries effort %q, want the selected %q", i+1, s.Effort, r.sel.Effort)
		}
	}
	for i, turn := range r.script {
		evs := providerEvents(r.events, i)
		if len(evs) == 0 {
			continue // the turn never ran; the request count says so
		}
		switch turn.end {
		case endNormal, endToolStop:
			r.checkNormalTurn(i, turn, evs)
		default:
			r.checkFailedTurn(i, evs)
		}
	}
}

// checkNormalTurn requires the event skeleton of a turn that ended normally to
// equal, type by type, the one provider/faux emits for the same blocks and stop
// reason, and the block-end events to carry whole payloads.
func (r *caseRun) checkNormalTurn(i int, turn logicalTurn, evs []core.Event) {
	r.tb.Helper()
	want, err := fauxSkeleton(turn)
	if err != nil {
		r.fail("turn %d: the faux reference: %v", i+1, err)
		return
	}
	if got := skeleton(evs); !slices.Equal(got, want) {
		r.fail("turn %d: the event skeleton is\n  %v\nwant provider/faux's\n  %v", i+1, got, want)
	}

	var ends []core.Event
	for _, e := range evs {
		switch e.(type) {
		case core.TextEndEvent, core.ThinkingEndEvent, core.ToolCallEndEvent:
			ends = append(ends, e)
		}
	}
	if len(ends) != len(turn.blocks) {
		r.fail("turn %d: %d block-end events for %d scripted blocks", i+1, len(ends), len(turn.blocks))
		return
	}
	for j, b := range turn.blocks {
		switch e := ends[j].(type) {
		case core.TextEndEvent:
			if b.kind != blockText || e.Text != b.text {
				r.fail("turn %d block %d: TextEndEvent holds %q, want the scripted %q", i+1, j, e.Text, b.text)
			}
		case core.ThinkingEndEvent:
			if b.kind != blockThinking || e.Thinking != b.text {
				r.fail("turn %d block %d: ThinkingEndEvent holds %q, want the scripted %q", i+1, j, e.Thinking, b.text)
			}
		case core.ToolCallEndEvent:
			if b.kind != blockToolCall || e.Block.ID != b.id || e.Block.Name != b.name || !jsonEqual(e.Block.Input, []byte(b.args)) {
				r.fail("turn %d block %d: ToolCallEndEvent holds %s %s %s, want the scripted %s %s %s",
					i+1, j, e.Block.ID, e.Block.Name, e.Block.Input, b.id, b.name, b.args)
			}
		}
	}
}

// checkFailedTurn requires the last two provider events of a turn that failed to
// be a terminal ErrorEvent, then a MessageEndEvent whose message has
// StopReasonError.
func (r *caseRun) checkFailedTurn(i int, evs []core.Event) {
	r.tb.Helper()
	if len(evs) < 2 {
		r.fail("turn %d: %d provider events, want at least a terminal ErrorEvent and a MessageEndEvent", i+1, len(evs))
		return
	}
	if ee, ok := evs[len(evs)-2].(core.ErrorEvent); !ok || !ee.Terminal {
		r.fail("turn %d: the second to last provider event is %#v, want a terminal ErrorEvent", i+1, evs[len(evs)-2])
	}
	if me, ok := evs[len(evs)-1].(core.MessageEndEvent); !ok || me.Message.StopReason != core.StopReasonError {
		r.fail("turn %d: the last provider event is %#v, want a MessageEndEvent with StopReasonError", i+1, evs[len(evs)-1])
	}
}

// providerEvents are the run's events after the TurnStartEvent of turn, up to
// and including the first MessageEndEvent after it. A turn the run never
// reached has none.
func providerEvents(events []core.Event, turn int) []core.Event {
	start := -1
	for i, e := range events {
		if ts, ok := e.(core.TurnStartEvent); ok && ts.TurnIndex == turn {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return nil
	}
	for i := start; i < len(events); i++ {
		if _, ok := events[i].(core.MessageEndEvent); ok {
			return events[start : i+1]
		}
	}
	return events[start:]
}

// skeleton is the event types of evs, without the MessageUpdateEvents and with
// each run of consecutive delta events of one block collapsed into one: what
// does not depend on how a vendor chunks its stream.
func skeleton(evs []core.Event) []string {
	var out []string
	prev := ""
	for _, e := range evs {
		if e.EventType() == core.EvMessageUpdate {
			continue
		}
		key := ""
		switch v := e.(type) {
		case core.TextDeltaEvent:
			key = fmt.Sprintf("%s#%d", e.EventType(), v.BlockIndex)
		case core.ThinkingDeltaEvent:
			key = fmt.Sprintf("%s#%d", e.EventType(), v.BlockIndex)
		case core.ToolInputDeltaEvent:
			key = fmt.Sprintf("%s#%d", e.EventType(), v.BlockIndex)
		}
		if key != "" && key == prev {
			continue
		}
		prev = key
		out = append(out, string(e.EventType()))
	}
	return out
}

// fauxEvents are the events provider/faux emits for the blocks and the stop
// reason of turn: the executable specification of the streaming protocol.
func fauxEvents(turn logicalTurn) ([]core.Event, error) {
	var blocks []core.ContentBlock
	for _, b := range turn.blocks {
		switch b.kind {
		case blockText:
			blocks = append(blocks, faux.FauxText(b.text))
		case blockThinking:
			blocks = append(blocks, faux.FauxThinking(b.text, b.signature))
		case blockToolCall:
			blocks = append(blocks, faux.FauxToolCall(b.id, b.name, b.args))
		}
	}
	stop := core.StopReasonStop
	if turn.end == endToolStop {
		stop = core.StopReasonToolUse
	}
	ctx, cancel := context.WithTimeout(context.Background(), caseTimeout)
	defer cancel()
	ch, err := faux.New(faux.FauxAssistantMessage(stop, blocks...)).Stream(ctx, core.Request{})
	if err != nil {
		return nil, err
	}
	var evs []core.Event
	for e := range core.EventStreamOf(ch, nil).Events() {
		evs = append(evs, e)
	}
	return evs, nil
}

// fauxSkeleton is the skeleton of fauxEvents.
func fauxSkeleton(turn logicalTurn) ([]string, error) {
	evs, err := fauxEvents(turn)
	if err != nil {
		return nil, err
	}
	return skeleton(evs), nil
}
