package ollama_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"runtime"
	"strings"
	"testing"

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/ollama"
)

// smokeArgs are the arguments the server sends for the call: five keys, in an
// order that is not alphabetical and that a map would not keep.
const smokeArgs = `{"zeta":"z","mu":"m","alpha":"a","omega":"o","beta":"b"}`

// smokeTool is a tool whose schema has those five properties, and keywords the
// SDK's tool type drops. It counts its runs.
func smokeTool(t *testing.T, runs *int) core.Tool {
	t.Helper()
	return core.Tool{
		Name: "read_file", Description: "reads a file",
		InputSchema: mustSchema(t, `{"type":"object","additionalProperties":false,"properties":{
			"zeta":{"type":"string","format":"uri"},"mu":{"type":"string"},"alpha":{"type":"string","minLength":1},
			"omega":{"type":"string"},"beta":{"type":"string"}},
			"required":["zeta","alpha"]}`),
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			*runs++
			return json.RawMessage(`{"text":"file contents"}`), nil
		},
	}
}

// smokeAgent builds a real agent on provider p with the tool, at the effort of
// the catalog's STANDARD tier.
func smokeAgent(t *testing.T, p core.ProviderClient, tool core.Tool) *agentkit.Agent {
	t.Helper()
	sel, err := catalog.Select("ollama", "STANDARD", "")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := agentkit.New(agentkit.Config{
		Client: p, Model: sel.Model.ID, Effort: sel.Effort,
		Toolset: agentkit.Toolset{Tools: []core.Tool{tool}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return agent
}

// smokeProvider builds the provider the way an embedder does: from the catalog's
// STANDARD row and the server's address in Options.Env.
func smokeProvider(t *testing.T, host string) *ollama.Client {
	t.Helper()
	sel, err := catalog.Select("ollama", "STANDARD", "")
	if err != nil {
		t.Fatal(err)
	}
	p, err := ollama.New(sel.Model, ollama.Options{Env: map[string]string{ollama.HostVar: host}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// checkFirstRequestShape checks what both requests of the loop carry: the
// catalog's model, window and effort, streaming, no truncation, and the tool
// with its schema cut to the SDK's subset.
func checkRequestShape(t *testing.T, srv *recorder, n int) {
	t.Helper()
	body := sentBody(t, srv, n)
	opts, _ := body["options"].(map[string]any)
	if body["model"] != "qwen3.8:27b" || opts["num_ctx"] != float64(32768) || body["truncate"] != false ||
		body["think"] != "medium" || body["stream"] != true {
		t.Errorf("request %d = model %v, options %v, truncate %v, think %v, stream %v; want the STANDARD row's",
			n, body["model"], opts, body["truncate"], body["think"], body["stream"])
	}
	tools := wireTools(t, body)
	if len(tools) != 1 {
		t.Fatalf("request %d holds %d tools, want 1", n, len(tools))
	}
	var want any
	_ = json.Unmarshal([]byte(`{"type":"object","required":["zeta","alpha"],"properties":{
		"zeta":{"type":"string"},"mu":{"type":"string"},"alpha":{"type":"string"},
		"omega":{"type":"string"},"beta":{"type":"string"}}}`), &want)
	if got := tools[0]["function"].(map[string]any)["parameters"]; !reflect.DeepEqual(got, want) {
		t.Errorf("request %d tool parameters = %s, want %s", n, mustJSON(t, got), mustJSON(t, want))
	}
}

// checkSecondRequest checks the replay: the first turn's call id, its arguments
// in the order the server sent them, the thinking, and the result paired to the
// id with the tool's name.
func checkSecondRequest(t *testing.T, srv *recorder) {
	t.Helper()
	var body struct {
		Messages []wireMessage `json:"messages"`
	}
	if err := json.Unmarshal(srv.Requests()[1].Body, &body); err != nil {
		t.Fatal(err)
	}
	var assistant, result *wireMessage
	for i, m := range body.Messages {
		switch m.Role {
		case "assistant":
			assistant = &body.Messages[i]
		case "tool":
			result = &body.Messages[i]
		}
	}
	if assistant == nil || result == nil || len(assistant.ToolCalls) != 1 {
		t.Fatalf("messages = %+v, want an assistant message with a call and a tool result", body.Messages)
	}
	call := assistant.ToolCalls[0]
	if call.ID != "call_1" || call.Function.Name != "read_file" || assistant.Thinking != "I should read the file" {
		t.Errorf("assistant = %+v, want call_1, read_file and the first turn's thinking", assistant)
	}
	if got := keyOrder(t, call.Function.Arguments); !reflect.DeepEqual(got, []string{"zeta", "mu", "alpha", "omega", "beta"}) {
		t.Errorf("arguments key order = %v, want the server's: zeta, mu, alpha, omega, beta", got)
	}
	if result.ToolCall != "call_1" || result.ToolName != "read_file" || !strings.Contains(result.Content, "file contents") {
		t.Errorf("tool message = %+v, want it paired to call_1 with the tool's name and output", result)
	}
}

// signRequests makes the SDK sign every request, as it does when OLLAMA_AUTH is
// set and a key is in place, so the layer that deletes Authorization has something
// to delete. It does nothing where the SDK does not find its key through $HOME.
func signRequests(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	home := t.TempDir()
	writeSigningKey(t, home)
	t.Setenv("HOME", home)
	t.Setenv("OLLAMA_AUTH", "1")
}

// TS-28-47 (smoke, 28-PATH-1): an agent completes a two-turn tool loop on a
// recording Ollama server, at zero cost.
func TestSmokeTwoTurnToolLoop_TS28_47(t *testing.T) {
	secrets := []string{"sk-ant-secret-1a2b", "sk-or-secret-3c4d", "ollama-secret-5e6f"}
	for i, name := range []string{"ANTHROPIC_API_KEY", "OPENROUTER_API_KEY", "OLLAMA_API_KEY"} {
		t.Setenv(name, secrets[i])
	}
	signRequests(t)
	srv := newRecorder(t,
		lines(chunk("", "I should read the file"), toolChunk("call_1", "read_file", smokeArgs), usageDone(354, 256, 120)),
		lines(chunk("The file says: file contents.", ""), usageDone(400, 300, 50)))
	var ran int
	agent := smokeAgent(t, smokeProvider(t, srv.URL()), smokeTool(t, &ran))

	res, err := agent.Run(context.Background(), "read a.txt")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FinalText() != "The file says: file contents." || res.StopReason != core.RunStopEndTurn || ran != 1 || srv.Count() != 2 {
		t.Fatalf("run = %q (%v), tool ran %d times, %d requests; want the turn-2 text, a normal end, 1 and 2",
			res.FinalText(), res.StopReason, ran, srv.Count())
	}
	checkRequestShape(t, srv, 0)
	checkRequestShape(t, srv, 1)
	checkSecondRequest(t, srv)
	checkCredentialFree(t, srv, secrets)
	for i, m := range res.Messages {
		// The agent reports the cost of the run itself; what the provider
		// reported is on the messages.
		if a, ok := m.(core.AssistantMessage); ok && (!a.Usage.Has(core.UsageCostUSD) || a.Usage.CostUSD != 0) {
			t.Errorf("message %d usage = %+v, want a cost of 0 that the provider reported", i, a.Usage)
		}
	}
	if u := res.Usage; u.CostUSD != 0 || !u.Has(core.UsageCostUSD) || u.InputTokens != 198 || u.CacheReadTokens != 556 || u.OutputTokens != 170 || u.Requests != 2 {
		t.Errorf("usage = %+v, want 198 in, 556 cache read, 170 out, 2 requests and a cost of 0 that is reported", u)
	}
}

// failedRun runs the agent on the prompt through Agent.Stream and returns the
// error events of the run, its result and its error, which is the error Run
// returns. The provider's typed error is on the run's error and on the
// ErrorEvent the loop forwards.
func failedRun(t *testing.T, agent *agentkit.Agent) ([]core.ErrorEvent, core.RunResult, error) {
	t.Helper()
	s, err := agent.Stream(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	var failures []core.ErrorEvent
	for e := range s.Events() {
		if ev, ok := e.(core.ErrorEvent); ok {
			failures = append(failures, ev)
		}
	}
	res, err := s.RunResult()
	return failures, res, err
}

// lastAssistant is the assistant message that ended a run.
func lastAssistant(t *testing.T, res core.RunResult) core.AssistantMessage {
	t.Helper()
	for i := len(res.Messages) - 1; i >= 0; i-- {
		if a, ok := res.Messages[i].(core.AssistantMessage); ok {
			return a
		}
	}
	t.Fatal("the run holds no assistant message")
	return core.AssistantMessage{}
}

// TS-28-48 (smoke, 28-PATH-2): an agent run on a server that answers 404, or on a
// closed port, ends with a named error and runs no tool.
func TestSmokeNamedErrors_TS28_48(t *testing.T) {
	cases := []struct {
		name     string
		host     func(*recorder) string
		reply    []reply
		sentinel error
		mention  string
	}{
		{"a 404", func(r *recorder) string { return r.URL() }, []reply{errorReply(404, "model 'qwen3.8:27b' not found")},
			ollama.ErrModelNotFound, "ollama pull qwen3.8:27b"},
		{"a 404 with no body", func(r *recorder) string { return r.URL() }, []reply{errorReply(404, "")},
			ollama.ErrModelNotFound, "ollama pull qwen3.8:27b"},
		{"a closed port", func(*recorder) string { return closedPortHost(t) }, nil, ollama.ErrUnreachable, "ollama serve"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newRecorder(t, tc.reply...)
			var ran int
			agent := smokeAgent(t, smokeProvider(t, tc.host(srv)), smokeTool(t, &ran))

			failures, res, err := failedRun(t, agent)
			if len(failures) != 1 || !failures[0].Terminal || !errors.Is(failures[0].Err, tc.sentinel) {
				t.Fatalf("error events = %+v, want one terminal event that wraps %v", failures, tc.sentinel)
			}
			if err == nil || !strings.Contains(err.Error(), tc.mention) || res.StopReason != core.RunStopError {
				t.Errorf("run error = %v (%v), want one that says %q", err, res.StopReason, tc.mention)
			}
			if !errors.Is(err, tc.sentinel) {
				t.Errorf("run error = %v, want one that wraps %v", err, tc.sentinel)
			}
			if a := lastAssistant(t, res); a.StopReason != core.StopReasonError || ran != 0 {
				t.Errorf("last message %q, tool ran %d times; want an error turn and no tool run", a.StopReason, ran)
			}
			if tc.reply != nil && srv.Count() != 1 {
				t.Errorf("the server received %d requests, want exactly 1", srv.Count())
			}
		})
	}
}

// TS-28-49 (smoke, 28-PATH-3): a stream cut before its done chunk ends the run
// without executing the partial tool call.
func TestSmokeTruncatedStream_TS28_49(t *testing.T) {
	srv := newRecorder(t, lines(chunk("partial answer", ""), toolChunk("call_1", "read_file", smokeArgs)))
	var ran int
	agent := smokeAgent(t, smokeProvider(t, srv.URL()), smokeTool(t, &ran))

	failures, res, err := failedRun(t, agent)
	if len(failures) != 1 || !errors.Is(failures[0].Err, ollama.ErrStreamTruncated) {
		t.Fatalf("error events = %+v, want one that wraps ErrStreamTruncated", failures)
	}
	if err == nil || !strings.Contains(err.Error(), ollama.ErrStreamTruncated.Error()) || !errors.Is(err, ollama.ErrStreamTruncated) {
		t.Errorf("run error = %v, want one that wraps the truncation", err)
	}
	a := lastAssistant(t, res)
	if a.Content.Text() != "partial answer" || len(core.ExtractToolUse(&a)) != 0 || ran != 0 {
		t.Errorf("message %q with %d tool calls, tool ran %d times; want the text, no call and no run",
			a.Content.Text(), len(core.ExtractToolUse(&a)), ran)
	}
}
