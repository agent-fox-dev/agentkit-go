package ollama_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/ollama"
)

// errorReply is a reply with an Ollama error body, or none when msg is "".
func errorReply(status int, msg string) reply {
	body := ""
	if msg != "" {
		body = fmt.Sprintf(`{"error":%q}`, msg)
	}
	return reply{status: status, header: map[string]string{"Content-Type": "application/json"}, body: body}
}

// failedTurn checks that a turn ended as a failure: StopReasonError, a terminal
// ErrorEvent, and an error text that starts with "ollama:". It returns the error.
func failedTurn(t *testing.T, o outcome) error {
	t.Helper()
	if o.err == nil {
		t.Fatal("the turn did not fail")
	}
	if !strings.HasPrefix(o.err.Error(), "ollama:") {
		t.Errorf("error %q does not start with \"ollama:\"", o.err)
	}
	if o.msg.StopReason != core.StopReasonError || o.msg.ErrorMessage != o.err.Error() {
		t.Errorf("message = {%q %q}, want an error turn with the error's text", o.msg.StopReason, o.msg.ErrorMessage)
	}
	var terminal bool
	for _, e := range o.events {
		if ev, ok := e.(core.ErrorEvent); ok && ev.Terminal {
			terminal = true
		}
	}
	if !terminal {
		t.Errorf("events %v hold no terminal ErrorEvent", kinds(o.events))
	}
	return o.err
}

// TS-28-32: a context that is already done is refused with its error and no
// channel, and no request is sent.
func TestACancelledContextIsRefused_TS28_32(t *testing.T) {
	srv := onePlainTurn(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ch, err := newClient(t, srv, testModel()).Stream(ctx, userRequest("hi"))
	if ch != nil || err != context.Canceled {
		t.Errorf("Stream = %v, %v; want a nil channel and context.Canceled", ch, err)
	}
	if srv.Count() != 0 {
		t.Errorf("the server received %d requests, want 0", srv.Count())
	}
}

// TS-28-33: a stream that ends without a done chunk ends with ErrStreamTruncated,
// keeps the text and drops the tool call.
func TestATruncatedStreamFails_TS28_33(t *testing.T) {
	srv := newRecorder(t, lines(chunk("partial", ""), toolChunk("call_1", "f", `{"a":1}`)))
	o := run(t, newClient(t, srv, testModel()), userRequest("hi"))

	if err := failedTurn(t, o); !errors.Is(err, ollama.ErrStreamTruncated) {
		t.Errorf("error = %v, want one that wraps ErrStreamTruncated", err)
	}
	if o.msg.Content.Text() != "partial" || len(core.ExtractToolUse(&o.msg)) != 0 {
		t.Errorf("message content = %#v, want the text and no tool call", o.msg.Content)
	}
}

// TS-28-34 (integration): cancelling mid-stream ends the turn within a second as
// aborted, with no ErrorEvent and no tool call.
func TestCancelAbortsTheTurn_TS28_34(t *testing.T) {
	srv := newRecorder(t, reply{
		status: 200, header: map[string]string{"Content-Type": "application/x-ndjson"},
		body: chunk("hello", "") + "\n" + toolChunk("call_1", "f", `{"a":1}`) + "\n", hold: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := newClient(t, srv, testModel()).Stream(ctx, userRequest("hi"))
	if err != nil {
		t.Fatal(err)
	}

	var o outcome
	for ev := range ch {
		o.events = append(o.events, ev.Event)
		if _, ok := ev.Event.(core.TextDeltaEvent); ok {
			cancel()
			break
		}
	}
	start := time.Now()
	finished := make(chan outcome, 1)
	go func() { finished <- drain(ch) }()
	var rest outcome
	select {
	case rest = <-finished:
	case <-time.After(time.Second):
		t.Fatal("the turn did not end within a second of the cancel")
	}
	t.Logf("ended %v after the cancel", time.Since(start))
	o.events = append(o.events, rest.events...)

	if rest.err != core.ErrAborted {
		t.Errorf("error = %v, want core.ErrAborted", rest.err)
	}
	if rest.msg.StopReason != core.StopReasonAborted || rest.msg.ErrorMessage != "Request was aborted" {
		t.Errorf("message = {%q %q}, want aborted with \"Request was aborted\"", rest.msg.StopReason, rest.msg.ErrorMessage)
	}
	if len(core.ExtractToolUse(&rest.msg)) != 0 || rest.msg.Content.Text() != "hello" {
		t.Errorf("content = %#v, want the text and no tool call", rest.msg.Content)
	}
	for _, e := range o.events {
		if _, isErr := e.(core.ErrorEvent); isErr {
			t.Errorf("an ErrorEvent was pushed: %v", kinds(o.events))
		}
	}
}

// closedPortHost is the address of a loopback port that was listened on and
// closed, so connecting to it is refused.
func closedPortHost(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return "http://" + addr
}

// TS-28-35 (integration): a refused connection ends the turn with
// ErrUnreachable and names the host, ollama serve and OLLAMA_HOST.
func TestARefusedConnectionIsUnreachable_TS28_35(t *testing.T) {
	host := closedPortHost(t)
	c, err := ollama.New(testModel(), ollama.Options{Host: host})
	if err != nil {
		t.Fatal(err)
	}
	o := run(t, c, userRequest("hi"))

	err = failedTurn(t, o)
	if !errors.Is(err, ollama.ErrUnreachable) {
		t.Errorf("error = %v, want one that wraps ErrUnreachable", err)
	}
	for _, want := range []string{host, "ollama serve", "OLLAMA_HOST"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	var opErr *net.OpError
	if !errors.As(err, &opErr) {
		t.Errorf("error %v does not keep the connection error", err)
	}
}

// TS-28-36 (integration): a 404 is ErrModelNotFound and a StatusError, naming the
// model, the host, Ollama's message and ollama pull; so is one with no body.
func TestA404IsModelNotFound_TS28_36(t *testing.T) {
	m := core.Model{ID: "qwen3.8:27b", ContextWindow: 8192}
	for name, rep := range map[string]reply{
		"with Ollama's message": errorReply(404, "model 'qwen3.8:27b' not found"),
		"with no body":          errorReply(404, ""),
	} {
		t.Run(name, func(t *testing.T) {
			srv := newRecorder(t, rep)
			o := run(t, newClient(t, srv, m), userRequest("hi"))
			err := failedTurn(t, o)
			var se *ollama.StatusError
			if !errors.Is(err, ollama.ErrModelNotFound) || !errors.As(err, &se) || se.Code != 404 {
				t.Fatalf("error = %v, want ErrModelNotFound and a StatusError with Code 404", err)
			}
			want := []string{"qwen3.8:27b", srv.URL(), "ollama pull qwen3.8:27b"}
			if name == "with Ollama's message" {
				want = append(want, "model 'qwen3.8:27b' not found")
			}
			for _, w := range want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not mention %q", err, w)
				}
			}
		})
	}
}

// TS-28-37 (integration): an error that says the input exceeds the context length,
// as a status error or inside a 200 stream, is ErrContextWindow, naming the model
// and the num_ctx sent and explaining the loaded-model caveat.
func TestContextWindowErrors_TS28_37(t *testing.T) {
	m := core.Model{ID: "ctx-model", ContextWindow: 32768}
	messages := []string{
		"the input length exceeds the context length",
		"input after truncation exceeds maximum context length",
		"input exceeds maximum context length and cannot be truncated further",
	}
	for _, msg := range messages {
		for name, rep := range map[string]reply{
			"a 400":         errorReply(400, msg),
			"in the stream": lines(chunk("x", ""), fmt.Sprintf(`{"error":%q}`, msg)),
		} {
			t.Run(name+": "+msg, func(t *testing.T) {
				o := run(t, newClient(t, newRecorder(t, rep), m), userRequest("hi"))
				err := failedTurn(t, o)
				if !errors.Is(err, ollama.ErrContextWindow) {
					t.Fatalf("error = %v, want one that wraps ErrContextWindow", err)
				}
				for _, want := range []string{"ctx-model", "32768", msg, "unloads"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q does not mention %q", err, want)
					}
				}
				// A status error is a StatusError with its code; an error line is not.
				var se *ollama.StatusError
				if isStatus := errors.As(err, &se); isStatus != (name == "a 400") || (isStatus && se.Code != 400) {
					t.Errorf("%s: StatusError %v (%+v), want it exactly for the 400, with Code 400", name, isStatus, se)
				}
			})
		}
	}
}

// TS-28-38 (integration): 401, 403 and other statuses, and an error line inside a
// 200 stream, are reported with the texts the spec gives.
func TestStatusErrors_TS28_38(t *testing.T) {
	for _, code := range []int{401, 403} {
		for name, rep := range map[string]reply{"with a message": errorReply(code, "denied"), "with no body": errorReply(code, "")} {
			t.Run(fmt.Sprintf("%d %s", code, name), func(t *testing.T) {
				srv := newRecorder(t, rep)
				err := failedTurn(t, run(t, newClient(t, srv, testModel()), userRequest("hi")))
				var se *ollama.StatusError
				if !errors.As(err, &se) || se.Code != code {
					t.Fatalf("error = %v, want a StatusError with Code %d", err, code)
				}
				for _, want := range []string{srv.URL(), "AgentKit sends no credential to Ollama", "OLLAMA_HOST"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q does not mention %q", err, want)
					}
				}
			})
		}
	}

	t.Run("500 with a message", func(t *testing.T) {
		err := failedTurn(t, run(t, newClient(t, newRecorder(t, errorReply(500, "boom")), testModel()), userRequest("hi")))
		var se *ollama.StatusError
		if !errors.As(err, &se) || se.Code != 500 || err.Error() != "ollama: HTTP 500: boom" {
			t.Errorf("error = %v (%T), want a StatusError{500} with the text \"ollama: HTTP 500: boom\"", err, err)
		}
	})
	t.Run("500 with no body", func(t *testing.T) {
		err := failedTurn(t, run(t, newClient(t, newRecorder(t, errorReply(500, "")), testModel()), userRequest("hi")))
		var se *ollama.StatusError
		if !errors.As(err, &se) || se.Code != 500 || err.Error() != "ollama: HTTP 500: Internal Server Error" {
			t.Errorf("error = %v, want the text \"ollama: HTTP 500: Internal Server Error\"", err)
		}
	})
	t.Run("an error line inside a 200 stream", func(t *testing.T) {
		rep := lines(chunk("so far", ""), `{"error":"model runner crashed"}`)
		o := run(t, newClient(t, newRecorder(t, rep), testModel()), userRequest("hi"))
		err := failedTurn(t, o)
		if !strings.Contains(err.Error(), "model runner crashed") || o.msg.Content.Text() != "so far" {
			t.Errorf("error %q, content %q; want Ollama's message and the text so far", err, o.msg.Content.Text())
		}
	})
}

// TS-28-39 (integration): every failure is one HTTP attempt, every error text
// starts "ollama:", and none holds the value of an environment variable.
func TestOneAttemptAndNoSecrets_TS28_39(t *testing.T) {
	secrets := []string{"sk-ant-secret-1a2b", "sk-or-secret-3c4d", "ollama-secret-5e6f"}
	for i, name := range []string{"ANTHROPIC_API_KEY", "OPENROUTER_API_KEY", "OLLAMA_API_KEY"} {
		t.Setenv(name, secrets[i])
	}
	var warnings []string
	warn := func(o *ollama.Options) {
		o.Warnf = func(f string, a ...any) { warnings = append(warnings, fmt.Sprintf(f, a...)) }
	}

	for _, status := range []int{404, 401, 429, 500, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			srv := newRecorder(t, errorReply(status, "failing"))
			o := run(t, newClient(t, srv, testModel(), warn), userRequest("hi"))
			err := failedTurn(t, o)
			if srv.Count() != 1 {
				t.Errorf("the server received %d requests, want exactly 1", srv.Count())
			}
			checkNoSecrets(t, o, err, secrets, warnings)
		})
	}
	t.Run("a closed port", func(t *testing.T) {
		c, err := ollama.New(testModel(), ollama.Options{Host: closedPortHost(t)})
		if err != nil {
			t.Fatal(err)
		}
		o := run(t, c, userRequest("hi"))
		checkNoSecrets(t, o, failedTurn(t, o), secrets, warnings)
	})
}

func checkNoSecrets(t *testing.T, o outcome, err error, secrets, warnings []string) {
	t.Helper()
	texts := append([]string{err.Error(), o.msg.ErrorMessage}, warnings...)
	for _, e := range o.events {
		if ev, ok := e.(core.ErrorEvent); ok {
			texts = append(texts, ev.Message)
		}
	}
	for _, text := range texts {
		for _, s := range secrets {
			if strings.Contains(text, s) {
				t.Errorf("%q holds the secret %q", text, s)
			}
		}
	}
}

// TS-28-42: a turn that fails before its done chunk reports one request and no
// token fields.
func TestFailedTurnsReportOnlyTheRequest_TS28_42(t *testing.T) {
	for name, rep := range map[string]reply{
		"a 500":     errorReply(500, "boom"),
		"truncated": lines(chunk("partial", "")),
	} {
		t.Run(name, func(t *testing.T) {
			o := run(t, newClient(t, newRecorder(t, rep), testModel()), userRequest("hi"))
			_ = failedTurn(t, o)
			u := o.msg.Usage
			if u.Requests != 1 || u.Has(core.UsageInputTokens) || u.Has(core.UsageOutputTokens) || u.Has(core.UsageCacheReadTokens) {
				t.Errorf("usage = %+v, want one request and no token field", u)
			}
		})
	}
}

// A connection reset while the body is read is a *net.OpError too, but the server
// was reached: it is not "unreachable", and the text so far is kept.
func TestAResetMidStreamIsNotUnreachable_TS28_35(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, chunk("so far", ""))
		w.(http.Flusher).Flush()
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		_ = conn.(*net.TCPConn).SetLinger(0)
		conn.Close()
	}))
	t.Cleanup(srv.Close)
	c, err := ollama.New(testModel(), ollama.Options{Host: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	o := run(t, c, userRequest("hi"))
	err = failedTurn(t, o)
	if errors.Is(err, ollama.ErrUnreachable) || strings.Contains(err.Error(), "ollama serve") {
		t.Errorf("error = %v: a reset mid-stream is not an unreachable server", err)
	}
	if o.msg.Content.Text() != "so far" {
		t.Errorf("content = %q, want the text so far", o.msg.Content.Text())
	}
}
