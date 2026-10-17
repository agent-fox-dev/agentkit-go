package mcpserve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/schema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// render is the whole text of a record: its level, message and every attribute,
// groups included, as a reader of the log would see it.
func render(r slog.Record) string {
	var b strings.Builder
	b.WriteString(r.Level.String() + " " + r.Message)
	var add func(prefix string, a slog.Attr)
	add = func(prefix string, a slog.Attr) {
		v := a.Value.Resolve()
		if v.Kind() == slog.KindGroup {
			for _, g := range v.Group() {
				add(prefix+a.Key+".", g)
			}
			return
		}
		fmt.Fprintf(&b, " %s%s=%s", prefix, a.Key, v.String())
	}
	r.Attrs(func(a slog.Attr) bool { add("", a); return true })
	return b.String()
}

// text is everything the capturing handler has been given, one record a line.
func (c *capture) text() string {
	var lines []string
	for _, r := range c.records() {
		lines = append(lines, render(r))
	}
	return strings.Join(lines, "\n")
}

// syncBuffer is a bytes.Buffer safe for the concurrent writers a log has.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// quiet sends the standard logger, which net/http uses for the errors it
// reports itself, and the process's stderr to buffers for the test. The
// originals come back with it.
func quiet(t *testing.T) (stdlog *syncBuffer, stderr *os.File) {
	t.Helper()
	stdlog = &syncBuffer{}
	log.SetOutput(stdlog)
	f, err := os.Create(filepath.Join(t.TempDir(), "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = f
	t.Cleanup(func() {
		os.Stderr = old
		log.SetOutput(os.Stderr)
		_ = f.Close()
	})
	return stdlog, f
}

func contents(t *testing.T, f *os.File) string {
	t.Helper()
	b, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// poke makes an authorized POST and ignores how it ends: a handler that panics
// closes the connection.
func poke(ep *Endpoint, body io.Reader) {
	req, err := http.NewRequest(http.MethodPost, ep.URL, body)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+ep.Token)
	req.Header.Set("Content-Type", "application/json")
	if res, err := httpClient.Do(req); err == nil {
		_ = res.Body.Close()
	}
}

// TS-23-44: Options has exactly the four fields, and a zero Options serves a
// call, a refusal, a rejected body, an HTTP server error and a stdio session
// without writing to stdout or stderr or through the standard logger.
// 23-REQ-8.1.
func TestZeroOptionsServesSilently_TS23_44(t *testing.T) {
	want := map[string]reflect.Type{
		"Name": reflect.TypeFor[string](), "Version": reflect.TypeFor[string](),
		"Instructions": reflect.TypeFor[string](), "Logger": reflect.TypeFor[*slog.Logger]()}
	rt := reflect.TypeFor[Options]()
	if rt.NumField() != len(want) {
		t.Errorf("Options has %d fields, want exactly %d", rt.NumField(), len(want))
	}
	for name, typ := range want {
		if f, ok := rt.FieldByName(name); !ok || f.Type != typ {
			t.Errorf("Options.%s is %v, want %v", name, f.Type, typ)
		}
	}

	stdlog, stderr := quiet(t)
	oldOut := os.Stdout
	outFile, err := os.Create(filepath.Join(t.TempDir(), "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = outFile
	defer func() { os.Stdout = oldOut }()

	s, _ := echoServer(t, Options{})
	ep := serveLoop(t, s)
	imported, err := dial(t, ep).Tools(context.Background(), nil)
	if err != nil || len(imported) != 1 {
		t.Fatalf("Tools = %v, %v", imported, err)
	}
	if res := imported[0].Execute(context.Background(), json.RawMessage(`{"text":"x"}`)); !res.OK {
		t.Errorf("a call failed: %+v", res)
	}
	if r := send(t, http.MethodPost, ep.URL, "Bearer wrong", strings.NewReader("{}")); r.status != http.StatusUnauthorized {
		t.Errorf("a refusal gave %d", r.status)
	}
	if r := send(t, http.MethodPost, ep.URL, "Bearer "+ep.Token, strings.NewReader(`{"a":1,"a":2}`)); r.status != http.StatusBadRequest {
		t.Errorf("a rejected body gave %d", r.status)
	}

	// A handler that panics: net/http recovers it and reports it to its own
	// logger, which must not be the process's stderr.
	old := newSDKHandler
	newSDKHandler = func(*sdk.Server) http.Handler {
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("panic text that must not be printed") })
	}
	crashing := serveLoop(t, s)
	newSDKHandler = old
	poke(crashing, strings.NewReader(initFrame))
	os.Stdout = oldOut

	toServer, fromServer := stdio(t)
	lines := frames(fromServer)
	done := serving(context.Background(), s)
	for _, f := range []string{initFrame, readyFrame, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`} {
		_, _ = io.WriteString(toServer, f+"\n")
	}
	_ = toServer.Close()
	if err := within(t, 5*time.Second, done, "ServeStdio"); err != nil {
		t.Errorf("ServeStdio returned %v", err)
	}
	for more := true; more; {
		select {
		case line := <-lines:
			if !strings.Contains(line, `"jsonrpc":"2.0"`) {
				t.Errorf("stdout carried %q, which is not a JSON-RPC message", line)
			}
		case <-time.After(300 * time.Millisecond):
			more = false
		}
	}

	if got := contents(t, stderr); got != "" {
		t.Errorf("stderr received %q", got)
	}
	if got := contents(t, outFile); got != "" {
		t.Errorf("stdout received %q outside the stdio session", got)
	}
	if got := stdlog.String(); got != "" {
		t.Errorf("the standard logger received %q", got)
	}
}

// TS-23-48 (property): no log record ever contains the token, the Authorization
// value, the arguments, the result text or the Detail, however they are chosen,
// including values that resemble the token. 23-REQ-8.5.
func TestNoSecretReachesTheLog_TS23_48(t *testing.T) {
	logs := newCapture()
	rng := rand.New(rand.NewPCG(23, 48))
	var current core.ToolResult
	var mu sync.Mutex
	tool := core.Tool{Name: "t", Description: "returns what the test chose", InputSchema: schema.Object(schema.Opt("k", schema.String())),
		Execute: func(context.Context, json.RawMessage) core.ToolResult {
			mu.Lock()
			defer mu.Unlock()
			return current
		}}
	rec := &recorder{toolRunner: runnerOf(t, tool)}
	s, err := newServer(rec, Options{Logger: slog.New(logs)})
	if err != nil {
		t.Fatal(err)
	}
	ep := serveLoop(t, s)
	pool := dial(t, ep)
	imported, err := pool.Tools(context.Background(), nil)
	if err != nil || len(imported) != 1 {
		t.Fatalf("Tools = %v, %v", imported, err)
	}

	marker := func(kind string) string { return fmt.Sprintf("%s-%08x%08x", kind, rng.Uint32(), rng.Uint32()) }
	var secrets []string
	secrets = append(secrets, ep.Token, "Bearer "+ep.Token)
	for i := range 30 {
		args, text, detail := marker("ARGS"), marker("TEXT"), marker("DETAIL")
		if i%3 == 0 {
			text = "echoing the token " + ep.Token // a result that resembles, indeed holds, the token
		}
		secrets = append(secrets, args, text, detail)
		mu.Lock()
		current = core.ToolResult{OK: i%2 == 0, Text: text, Detail: detail}
		if !current.OK {
			current.Error = "handler_error"
		}
		mu.Unlock()
		arguments, _ := json.Marshal(map[string]string{"k": args})
		imported[0].Execute(context.Background(), arguments)

		// A refused request that carries most of the token, and a rejected body
		// that carries the arguments, one of them as a key.
		send(t, http.MethodPost, ep.URL, "Bearer "+ep.Token[:40]+"zzzzzzzz", strings.NewReader("{}"))
		send(t, http.MethodPost, ep.URL, "Bearer "+ep.Token, strings.NewReader(`{"`+args+`":1,"`+args+`":"`+text+`"}`))
	}

	// A failing call whose error code is text, not a code.
	const odd = "this is not an error code, it is a sentence with a secret"
	mu.Lock()
	current = core.ToolResult{OK: false, Error: odd, Detail: "d"}
	mu.Unlock()
	imported[0].Execute(context.Background(), json.RawMessage(`{}`))
	secrets = append(secrets, "sentence with a secret")

	// A stdio session that ends on a frame whose key and value are the arguments.
	toServer, _ := stdio(t)
	stdioArgs := marker("ARGS")
	secrets = append(secrets, stdioArgs)
	done := serving(context.Background(), s)
	go func() {
		for _, f := range []string{initFrame, readyFrame, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"t","arguments":{"` + stdioArgs + `":1,"` + stdioArgs + `":2}}}`} {
			if _, err := io.WriteString(toServer, f+"\n"); err != nil {
				return
			}
		}
	}()
	if err := within(t, 5*time.Second, done, "ServeStdio on a bad frame"); err == nil {
		t.Error("the bad frame did not end the session")
	}

	out := logs.text()
	if len(logs.records()) < 60 {
		t.Fatalf("only %d records were logged, so the property compared little:\n%s", len(logs.records()), out)
	}
	for _, secret := range secrets {
		if strings.Contains(out, secret) {
			t.Errorf("the log contains %q", secret)
		}
	}
}

// TS-23-44, the server's own errors: a recovered handler panic is reported
// through Options.Logger as a fixed message, with no text of the panic.
func TestServerErrorsGoThroughTheLogger_TS23_44(t *testing.T) {
	quiet(t)
	logs := newCapture()
	s, _ := echoServer(t, Options{Logger: slog.New(logs)})
	old := newSDKHandler
	newSDKHandler = func(*sdk.Server) http.Handler {
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("panic text that must not be logged") })
	}
	ep := serveLoop(t, s)
	newSDKHandler = old
	poke(ep, strings.NewReader(initFrame))

	var found bool
	for _, r := range logs.records() {
		if r.Message == "http server error" && r.Level == slog.LevelWarn {
			found = true
		}
	}
	if !found {
		t.Errorf("no Warn record reports the server error:\n%s", logs.text())
	}
	if strings.Contains(logs.text(), "must not be logged") {
		t.Errorf("the log carries the panic's text:\n%s", logs.text())
	}
}
