package agentkit

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
)

// errNoSuchModel and *statusFailure stand for a provider's own sentinel and
// error type, the pair a caller matches with errors.Is and errors.As.
var errNoSuchModel = errors.New("test: no such model")

type statusFailure struct{ Code int }

func (e *statusFailure) Error() string { return fmt.Sprintf("test: HTTP %d", e.Code) }

// failedAgent is an agent over a provider whose only turn fails with err after
// a partial text, the way a provider ends a stream it cannot finish.
func failedAgent(t *testing.T, err error) *Agent {
	t.Helper()
	p := faux.New(faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("partial")}, Err: err})
	a, nerr := New(Config{Client: p, Model: testModelID})
	if nerr != nil {
		t.Fatal(nerr)
	}
	return a
}

// TestARunErrorWrapsTheProvidersError: a turn that ends in an error leaves the
// run with an error that still matches the provider's sentinel and type, with
// the text the run always had: the turn's ErrorMessage.
func TestARunErrorWrapsTheProvidersError(t *testing.T) {
	cause := fmt.Errorf("%w: %w", errNoSuchModel, &statusFailure{Code: 404})
	res, err := failedAgent(t, cause).Run(context.Background(), "go")

	if !errors.Is(err, errNoSuchModel) {
		t.Errorf("Run error = %v, want one that wraps the provider's sentinel", err)
	}
	var sf *statusFailure
	if !errors.As(err, &sf) || sf.Code != 404 {
		t.Errorf("Run error = %v, want one that holds the provider's *statusFailure with code 404", err)
	}
	if err == nil || err.Error() != cause.Error() {
		t.Errorf("Run error text = %q, want the turn's ErrorMessage %q", err, cause.Error())
	}
	if res.StopReason != core.RunStopError || !errors.Is(res.Error, errNoSuchModel) || res.Error != err {
		t.Errorf("result stop %q, Error %v; want RunStopError and the error Run returned", res.StopReason, res.Error)
	}
	if am := lastAssistant(res.Messages); am == nil || am.StopReason != core.StopReasonError || am.ErrorMessage != cause.Error() {
		t.Errorf("last assistant message = %+v, want an error turn that keeps its ErrorMessage %q", am, cause)
	}
}

// TestAStreamedRunKeepsTheProvidersErrorToo: the run's error is the same one on
// the stream's result and on the AgentDoneEvent.
func TestAStreamedRunKeepsTheProvidersErrorToo(t *testing.T) {
	s, err := failedAgent(t, fmt.Errorf("%w: gone", errNoSuchModel)).Stream(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	var done *core.AgentDoneEvent
	for e := range s.Events() {
		if d, ok := e.(core.AgentDoneEvent); ok {
			done = &d
		}
	}
	_, rerr := s.RunResult()
	if !errors.Is(rerr, errNoSuchModel) {
		t.Errorf("RunResult error = %v, want one that wraps the provider's sentinel", rerr)
	}
	if done == nil || !errors.Is(done.Result.Error, errNoSuchModel) {
		t.Errorf("AgentDoneEvent = %+v, want a result error that wraps the provider's sentinel", done)
	}
}

// TestAFailedTurnWithoutAProviderErrorKeepsItsText: a provider that marks the
// turn failed but sets no error still gives the run an error with the message.
func TestAFailedTurnWithoutAProviderErrorKeepsItsText(t *testing.T) {
	s := &scripted{turns: []core.AssistantMessage{{StopReason: core.StopReasonError, ErrorMessage: "overloaded"}}}
	_, err := newTestAgent(t, s, nil).Run(context.Background(), "go")
	if err == nil || err.Error() != "overloaded" {
		t.Fatalf("Run error = %v, want the message %q", err, "overloaded")
	}
	if cause := errors.Unwrap(err); cause != nil {
		t.Errorf("Run error wraps %v, want nothing: the provider gave no error", cause)
	}
}

// TestTheRunErrorReadsAsTheTurnsMessage: where the turn's ErrorMessage and the
// provider's error say different things, the run's text is the message's, as it
// was before the error was kept, and the error is still matchable.
func TestTheRunErrorReadsAsTheTurnsMessage(t *testing.T) {
	// The provider names its model, so the loop returns the message as it came.
	msg := core.AssistantMessage{StopReason: core.StopReasonError, ErrorMessage: "the model is not on the server", Model: "served-model"}
	p := streamFunc(func(context.Context, core.Request) *core.EventStream {
		s := core.NewEventStream(core.StreamOptions{})
		s.Push(core.MessageEndEvent{Message: msg})
		s.End(core.StreamResult{Message: &msg, Err: errNoSuchModel})
		return s
	})
	a, err := New(Config{Client: p, Model: testModelID})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Run(context.Background(), "go")
	if err == nil || err.Error() != msg.ErrorMessage || !errors.Is(err, errNoSuchModel) {
		t.Fatalf("Run error = %v, want the text %q over the provider's sentinel", err, msg.ErrorMessage)
	}
}

// messagelessFailure is a provider whose stream carries an error and no message.
type messagelessFailure struct{ err error }

func (p messagelessFailure) Stream(context.Context, core.Request) (<-chan core.StreamEvent, error) {
	ch := make(chan core.StreamEvent, 1)
	ch <- core.StreamEvent{Err: p.err}
	close(ch)
	return ch, nil
}

// TestAStreamWithNoMessageStillWrapsTheProvidersError: the loop makes the
// terminal message itself when the provider sent none, and the provider's error
// is still the run's cause.
func TestAStreamWithNoMessageStillWrapsTheProvidersError(t *testing.T) {
	a, err := New(Config{Client: messagelessFailure{err: fmt.Errorf("%w: cut", errNoSuchModel)}, Model: testModelID})
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Run(context.Background(), "go")
	if !errors.Is(err, errNoSuchModel) || res.StopReason != core.RunStopError {
		t.Fatalf("Run error = %v, stop %q; want RunStopError and an error that wraps the provider's sentinel", err, res.StopReason)
	}
	if am := lastAssistant(res.Messages); am == nil || am.ErrorMessage != err.Error() {
		t.Errorf("last assistant message = %+v, want one whose ErrorMessage is the run error %q", am, err)
	}
}
