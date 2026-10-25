package agentkit

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
)

func TestAgent_ResolutionIsWhatResolveChoseAndACopy(t *testing.T) {
	var provider, id string
	for _, v := range catalog.Default().Vendors() {
		for _, m := range catalog.Default().Models(v) {
			if sel, err := catalog.Select(v, m, ""); err == nil && len(sel.Model.Efforts) > 0 && len(sel.Model.Compat) > 0 {
				provider, id = v, m
			}
		}
	}
	if id == "" {
		t.Fatal("the catalog has no row with both Efforts and Compat")
	}
	fp := faux.New()
	cfg := Config{Client: fp, Provider: provider, Model: id}
	want, err := Resolve(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a := mustNew(t, cfg)
	got := a.Resolution()
	if !reflect.DeepEqual(got, want) || got.Client != core.ProviderClient(fp) {
		t.Fatalf("Resolution = %+v, want Resolve's %+v", got, want)
	}
	for k, v := range got.Model.Efforts {
		if v != nil {
			*v = "tampered"
		}
		got.Model.Efforts[k] = nil
	}
	got.Model.Compat[0] ^= 0xff
	got.Model.ID = "tampered"
	if again := a.Resolution(); !reflect.DeepEqual(again.Model, want.Model) || !reflect.DeepEqual(a.model, want.Model) {
		t.Errorf("a change to the returned row reached the agent: %+v", again.Model)
	}
}

func TestAgent_RunsOnePromptAtATime(t *testing.T) {
	fp := faux.New(faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("slow")}, Delay: time.Minute})
	a := mustNew(t, Config{Client: fp})
	ctx, cancel := context.WithCancel(context.Background())
	first, err := a.Stream(ctx, "first")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(context.Background(), "second"); !errors.Is(err, core.ErrBusy) {
		t.Errorf("Run during a run = %v, want ErrBusy", err)
	}
	if _, err := a.Stream(context.Background(), "third"); !errors.Is(err, core.ErrBusy) {
		t.Errorf("Stream during a run = %v, want ErrBusy", err)
	}
	testutil.Eventually(t, "the first request", func() bool { return fp.Calls() == 1 })
	cancel()
	if _, err := first.RunResult(); !errors.Is(err, context.Canceled) {
		t.Fatalf("the first run = %v", err)
	}
	for i := range 200 { // the slot is free by the time a run returns
		if _, err := a.Run(context.Background(), "next"); err != nil {
			t.Fatalf("run %d after the first = %v", i, err)
		}
	}
}

func TestAgent_KeepsTheTranscriptAndUsageAcrossRuns(t *testing.T) {
	u := reported(100, 10)
	a, _ := scripted(t, Toolset{}, faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("one")}, Usage: u},
		faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("two")}, Usage: u})
	if _, err := a.Run(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	evs, res, err := drain(t, context.Background(), a, "second")
	if err != nil || res.Usage.InputTokens != 100 || len(res.Messages) != 2 {
		t.Fatalf("the second run = %+v, %v; want only its own usage and messages", res, err)
	}
	done := evs[len(evs)-1].(core.AgentDoneEvent)
	if a.Usage().InputTokens != 200 || done.Usage.InputTokens != 200 || done.Result.Usage.InputTokens != 100 {
		t.Errorf("lifetime usage %d, done event %d/%d; want 200 and the run's 100", a.Usage().InputTokens, done.Usage.InputTokens, done.Result.Usage.InputTokens)
	}
	if msgs := a.Messages(); len(msgs) != 4 || msgs[2].(core.UserMessage).Content.Text() != "second" {
		t.Errorf("the transcript holds %d messages, want both runs'", len(msgs))
	}
}

// TestAgent_StateAndRunnerAreUsableDuringARun reads the agent and calls its
// runner from inside a run's own tool call: nothing deadlocks, and the
// runner's calls stay out of the run.
func TestAgent_StateAndRunnerAreUsableDuringARun(t *testing.T) {
	var a *Agent
	var events eventLog
	var during struct {
		msgs   int
		res    core.ToolResult
		busy   error
		runner *Runner
	}
	look := tool("look", func(ctx context.Context, _ json.RawMessage) core.ToolResult {
		during.msgs = len(a.Messages())
		during.runner = a.Runner()
		during.res = a.Runner().Call(context.Background(), "echo", nil)
		_, during.busy = a.Stream(context.Background(), "again")
		_, _ = a.Usage(), a.Resolution()
		return core.OKResult(nil)
	})
	a, _ = scripted(t, Toolset{Tools: []core.Tool{look, echo("echo", nil)}, Events: events.add},
		faux.Turn{Blocks: []core.ContentBlock{call("c1", "look", "{}")}, StopReason: core.StopReasonToolUse, Usage: reported(10, 5)}, faux.Text("done"))
	evs, res, err := drain(t, context.Background(), a, "go")
	if err != nil || during.msgs != 2 || !during.res.OK || !errors.Is(during.busy, core.ErrBusy) || during.runner != a.Runner() {
		t.Fatalf("during the run: %+v; run %v", during, err)
	}
	if len(res.Messages) != 4 || res.Usage.InputTokens != 10 {
		t.Errorf("the runner's call reached the run: %d messages, %+v", len(res.Messages), res.Usage)
	}
	runStarts, _, _ := executions(evs)
	sinkStarts, _, _ := executions(events.all())
	if len(runStarts) != 1 || runStarts["c1"] != 1 || len(sinkStarts) != 1 || sinkStarts["c1"] != 0 {
		t.Errorf("the run's stream saw %v and Toolset.Events %v; want the run's call on the stream and the runner's on Events", runStarts, sinkStarts)
	}
}
