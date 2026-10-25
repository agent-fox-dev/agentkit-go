package mcpserve

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/wire"
)

// The call is still running when stdin closes, and its reply still goes out.
func TestAtTheEndOfInputServeStdioWritesTheRepliesOwedClosesStdinAndReturnsNil(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	slow := tool("slow", func(context.Context, json.RawMessage) core.ToolResult {
		close(started)
		<-release
		return core.OKResult(map[string]any{"late": true})
	})
	s, _ := serverOf(t, Options{}, slow)
	c := serveStdio(t, context.Background(), s)
	c.send(initFrame, readyFrame, call("1", `{"name":"slow"}`))
	c.lines.Scan()
	<-started
	_ = c.in.Close()
	close(release)
	if !c.lines.Scan() || !strings.Contains(c.lines.Text(), `"id":1,"result"`) || !strings.Contains(c.lines.Text(), "late") {
		t.Errorf("the reply owed is %s", c.lines.Text())
	}
	if err := <-c.done; err != nil {
		t.Errorf("ServeStdio returned %v, want nil", err)
	}
	if _, err := os.Stdin.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
		t.Errorf("a read of stdin gives %v, want os.ErrClosed", err)
	}
	if _, err := io.WriteString(os.Stdout, "x\n"); err != nil || !c.lines.Scan() || c.lines.Text() != "x" {
		t.Errorf("stdout is closed (%v) or carried more than the frames: %q", err, c.lines.Text())
	}
}

func TestServeStdioWithADoneContextReadsNothing(t *testing.T) {
	r, w, _ := os.Pipe()
	defer w.Close()
	_, _ = io.WriteString(w, "unread\n")
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old; _ = r.Close() }()
	s, _ := serverOf(t, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := make([]byte, 7)
	if err := s.ServeStdio(ctx); err != context.Canceled || string(got[:must(io.ReadFull(r, got))]) != "unread\n" {
		t.Errorf("ServeStdio returned %v and left %q on stdin, want context.Canceled and what was written", err, got)
	}
}

func TestAFrameThatBreaksTheWireLimitsEndsTheSessionUnserved(t *testing.T) {
	for rule, args := range map[wire.Rule]string{
		wire.RuleDuplicateKey: `{"text":"a","text":"b"}`,
		wire.RuleDepth:        `{"x":` + strings.Repeat("[", 70) + strings.Repeat("]", 70) + `}`,
		wire.RuleMessageBytes: `{"text":"` + strings.Repeat("a", 17<<20) + `"}`,
	} {
		s, rec := serverOf(t, Options{})
		c := serveStdio(t, context.Background(), s)
		c.send(initFrame, readyFrame, call("1", `{"name":"echo","arguments":`+args+`}`))
		err := <-c.done
		var we *wire.Error
		if !strings.HasPrefix(err.Error(), "mcpserve: ") || !errors.As(err, &we) || we.Rule != rule || len(rec.results()) != 0 {
			t.Errorf("%s: ServeStdio returned %v after %d runner calls, want an mcpserve error for the rule and none", rule, err, len(rec.results()))
		}
	}
}
