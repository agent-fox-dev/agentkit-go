package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
)

const (
	// maxRequestBytes bounds the request file, as wire.Defaults() bounds an
	// MCP message.
	maxRequestBytes = 16 << 20
	// defaultTimeout bounds a call when --timeout is absent.
	defaultTimeout = 10 * time.Minute
	// timeoutGrace is how long past the deadline the command waits for a
	// cancelled call before it writes the timeout result anyway, so a tool that
	// ignores cancellation does not hold a relay past its bound.
	timeoutGrace = 5 * time.Second
)

// request is a call read from the request file.
type request struct {
	name  string
	input json.RawMessage
}

// readRequest reads the request file: a JSON object of at most 16 MiB with a
// non-empty string name and an optional input, passed to the runner as it is.
func readRequest(path string) (request, error) {
	if path == "" {
		return request{}, errors.New("agentkit-tools call: --request <file> is required")
	}
	f, err := os.Open(path)
	if err != nil {
		return request{}, fmt.Errorf("agentkit-tools call: reading the request: %v", err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxRequestBytes+1))
	if err != nil {
		return request{}, fmt.Errorf("agentkit-tools call: reading the request: %v", err)
	}
	if len(b) > maxRequestBytes {
		return request{}, fmt.Errorf("agentkit-tools call: the request %s is larger than 16 MiB", path)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(b, &obj); err != nil || obj == nil {
		return request{}, fmt.Errorf("agentkit-tools call: the request %s is not a JSON object", path)
	}
	var name string
	if raw, ok := obj["name"]; !ok || json.Unmarshal(raw, &name) != nil || name == "" {
		return request{}, fmt.Errorf("agentkit-tools call: the request %s has no non-empty string name", path)
	}
	return request{name: name, input: obj["input"]}, nil
}

// callOutput is what call writes: the result as core.ToolResult marshals it,
// and the text a session would serve for the same call.
type callOutput struct {
	core.ToolResult
	Text string `json:"text"`
}

// servedText is the text an MCP session serves for res: the text the model
// reads, then the text of each text block, joined with a newline.
func servedText(res core.ToolResult) string {
	parts := []string{res.LLMText()}
	for _, b := range res.Blocks {
		if tb, ok := b.(core.TextBlock); ok {
			parts = append(parts, tb.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// runCall makes one tool call and writes its result. The call ran when the
// command exits 0, whether the tool succeeded or not.
func runCall(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("call", flag.ContinueOnError)
	var s settings
	s.register(fs)
	reqPath := fs.String("request", "", "")
	timeoutArg := fs.String("timeout", defaultTimeout.String(), "")
	if parse(fs, args, stderr) != nil {
		return 2
	}
	if err := s.checkDir(); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	timeout, err := time.ParseDuration(*timeoutArg)
	if err != nil || timeout <= 0 {
		fmt.Fprintf(stderr, "agentkit-tools call: --timeout must be a positive duration such as 30s or 10m, not %q\n", *timeoutArg)
		return 2
	}
	req, err := readRequest(*reqPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	srv, err := open(s, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitCode(err)
	}
	defer srv.close()

	ctx, signalled, stop := withSignals(context.Background())
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	done := make(chan core.ToolResult, 1)
	go func() { done <- srv.runner.Call(ctx, req.name, req.input) }()

	var res core.ToolResult
	select {
	case res = <-done:
	case <-ctx.Done():
		if code := signalled(); code != 0 {
			return code
		}
		select {
		case <-done:
		case <-time.After(timeoutGrace):
		}
	}
	if code := signalled(); code != 0 {
		return code
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		res = core.ErrResult("timeout", fmt.Sprintf("the call did not finish within %s", timeout))
	}
	b, err := json.Marshal(callOutput{ToolResult: res, Text: servedText(res)})
	if err != nil {
		fmt.Fprintf(stderr, "agentkit-tools call: the result: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s\n", b)
	return 0
}

// withSignals is ctx, cancelled when SIGINT or SIGTERM arrives, with the exit
// status that signal asks for (130 or 143, 0 while none has) and the function
// that stops listening.
func withSignals(ctx context.Context) (context.Context, func() int, func()) {
	ctx, cancel := context.WithCancel(ctx)
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	var status atomic.Int32
	go func() {
		select {
		case sig := <-ch:
			if sig == syscall.SIGTERM {
				status.Store(143)
			} else {
				status.Store(130)
			}
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, func() int { return int(status.Load()) }, func() {
		signal.Stop(ch)
		cancel()
	}
}
