package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func secretsOf(m map[string]string) func(string) string {
	return func(name string) string { return m[name] }
}

// The child gets exactly the reduced environment plus its declared env, with
// ${VAR} resolved at spawn time from the secrets store, then the environment;
// a variable set to the empty string is a value, not an unresolved reference.
func TestAStdioServerGetsExactlyItsResolvedEnvironment(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "parent-only")
	p := NewPool(ConnectionOptions{})
	t.Cleanup(func() { _ = p.Close() })
	conn, err := spawn(t, p, "serve", ServerConfig{Name: "child",
		Env: map[string]string{"SUPPLIED": "${A_SECRET}", "BLANK": "[${EMPTY}]", "FROM_ENV": "$BASE"}},
		[]string{"EMPTY=", "BASE=base-value"}, secretsOf(map[string]string{"A_SECRET": "resolved-at-spawn"}))
	must(t, err)
	res, err := conn.Call(context.Background(), "env", nil)
	must(t, err)
	got := strings.Split(firstText(t, res), "\n")
	want := []string{"AGENTKIT_MCP_CHILD=serve", "EMPTY=", "BASE=base-value",
		"BLANK=[]", "FROM_ENV=base-value", "SUPPLIED=resolved-at-spawn"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("child env = %q, want %q", got, want)
	}
}

// An unset ${VAR} is a configuration error naming every unresolved reference
// (and never a resolved secret), and nothing is spawned or requested.
func TestAnUnresolvedReferenceRefusesTheConnect(t *testing.T) {
	var got http.Header
	hs := remote(t, echoServer(), "k", &got)
	secrets := secretsOf(map[string]string{"GH_TOKEN": "ghp_secret"})
	for _, tc := range []struct {
		name string
		cfg  ServerConfig
		want []string
	}{
		{"in a child's env", ServerConfig{Name: "gh", Command: "true", Env: map[string]string{
			"TOKEN": "${GH_TOKEN}", "MODE": "${MISSING}-suffix", "OTHER": "$ALSO_MISSING"}},
			[]string{"MISSING", "ALSO_MISSING"}},
		{"in a header", ServerConfig{Name: "gh", URL: hs.URL, Headers: map[string]string{
			"Authorization": "Bearer ${MISSING}", "X-Other": "${MISSING}", "X-Token": "${GH_TOKEN}"}},
			[]string{"MISSING"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := NewPool(ConnectionOptions{})
			defer p.Close()
			_, err := p.Connect(context.Background(), tc.cfg, []string{"PATH=/usr/bin"}, secrets)
			var unresolved *UnresolvedVariableError
			if !errors.As(err, &unresolved) || !reflect.DeepEqual(unresolved.Variables, tc.want) {
				t.Fatalf("err = %v, want an UnresolvedVariableError naming %v", err, tc.want)
			}
			if !strings.Contains(err.Error(), "${MISSING}") || strings.Contains(err.Error(), "ghp_secret") {
				t.Fatalf("the message must name the variable and never the resolved secret: %v", err)
			}
			if len(p.Names()) != 0 || got != nil {
				t.Fatal("nothing may be spawned or requested on a configuration error")
			}
		})
	}
}

// A remote server's headers resolve through the same ${VAR} path as a child's
// environment.
func TestARemoteServersHeadersCarryTheResolvedSecret(t *testing.T) {
	var got http.Header
	hs := remote(t, echoServer(), "ghp_secret", &got)
	p := NewPool(ConnectionOptions{})
	t.Cleanup(func() { _ = p.Close() })
	conn, err := p.Connect(context.Background(), ServerConfig{Name: "remote", URL: hs.URL,
		Headers: map[string]string{"Authorization": "Bearer ${GH_TOKEN}"}},
		nil, secretsOf(map[string]string{"GH_TOKEN": "ghp_secret"}))
	must(t, err)
	if h := got.Get("Authorization"); h != "Bearer ghp_secret" {
		t.Fatalf("Authorization was %q", h)
	}
	res, err := conn.Call(context.Background(), "echo", map[string]any{"message": "over http"})
	must(t, err)
	if firstText(t, res) != "echo: over http" {
		t.Fatalf("result = %+v", res)
	}
}

// A server on the HTTP+SSE transport connects and answers calls.
func TestAnSSEServerConnectsAndAnswers(t *testing.T) {
	srv := echoServer()
	hs := httptest.NewServer(sdk.NewSSEHandler(func(*http.Request) *sdk.Server { return srv }, nil))
	t.Cleanup(hs.Close)
	p := NewPool(ConnectionOptions{})
	t.Cleanup(func() { _ = p.Close() })
	conn, err := p.Connect(context.Background(), ServerConfig{Name: "legacy", URL: hs.URL, Transport: "sse"}, nil, nil)
	must(t, err)
	res, err := conn.Call(context.Background(), "echo", map[string]any{"message": "over sse"})
	must(t, err)
	if firstText(t, res) != "echo: over sse" {
		t.Fatalf("result = %+v", res)
	}
}

// A header whose resolved value is blank or carries a control byte (a
// request-splitting attempt that can arrive through a secret) is withheld and
// reported, not handed to net/http.
func TestAnUnsafeOrBlankHeaderIsWithheldAndReported(t *testing.T) {
	var got http.Header
	hs := remote(t, echoServer(), "k", &got)
	var w warnings
	p := NewPool(ConnectionOptions{Warnf: w.warnf})
	t.Cleanup(func() { _ = p.Close() })
	_, err := p.Connect(context.Background(), ServerConfig{Name: "remote", URL: hs.URL, Headers: map[string]string{
		"X-Token": "abc${INJECT}", "X-Blank": "${EMPTY} ", "Bad\nName": "v", "X-API-Key": "k"}},
		[]string{"EMPTY="}, secretsOf(map[string]string{"INJECT": "def\r\nX-Smuggled: yes"}))
	must(t, err)
	for _, h := range []string{"X-Token", "X-Blank", "X-Smuggled"} {
		if _, sent := got[h]; sent {
			t.Fatalf("header %s reached the server: %v", h, got)
		}
	}
	for _, h := range []string{`"X-Token"`, `"X-Blank"`, `"Bad\nName"`} {
		if !w.has(h + " resolved to a blank or unsafe value") {
			t.Fatalf("no warning for %s: %q", h, w.list)
		}
	}
}

// A file:// or custom-scheme endpoint is a mistake or an attempt to make the
// client read something local; a server with no endpoint at all is refused too.
func TestPoolConnectRefusesAServerWithoutAnHTTPOrCommandEndpoint(t *testing.T) {
	p := NewPool(ConnectionOptions{})
	for _, url := range []string{"file:///etc/passwd", "ftp://h/x", "ws://h/x", "::", ""} {
		if _, err := p.Connect(context.Background(), ServerConfig{Name: "x", URL: url}, nil, nil); err == nil {
			t.Fatalf("url %q must be refused", url)
		}
	}
}

// The configured headers carry the server's credential, and net/http forwards
// them to wherever a redirect points.
func TestARedirectIsNotFollowed(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(target.Close)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(origin.Close)

	p := NewPool(ConnectionOptions{})
	t.Cleanup(func() { _ = p.Close() })
	_, err := p.Connect(context.Background(), ServerConfig{Name: "r", URL: origin.URL, Timeout: 3 * time.Second,
		Headers: map[string]string{"X-Api-Key": "the-secret"}}, nil, nil)
	if err == nil || hits.Load() != 0 {
		t.Fatalf("err = %v, redirect target hit %d time(s); want a failed connect and no hit", err, hits.Load())
	}
}

// A server that would shadow a declared native tool is refused at connect,
// before the wrong tool can run; the default prefix keeps the two apart.
func TestAServerShadowingANativeToolIsRefusedAtConnect(t *testing.T) {
	hs := remote(t, echoServer(), "k", nil)
	for _, tc := range []struct {
		name    string
		disable bool
		refused bool
	}{
		{"unprefixed", true, true},
		{"prefixed", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := NewPool(ConnectionOptions{})
			p.NativeTools = []string{"echo"}
			t.Cleanup(func() { _ = p.Close() })
			conn, err := p.Connect(context.Background(), ServerConfig{Name: "remote", URL: hs.URL,
				DisablePrefix: tc.disable, Headers: map[string]string{"X-API-Key": "k"}}, nil, nil)
			if tc.refused != errors.Is(err, ErrNameCollision) {
				t.Fatalf("err = %v, want ErrNameCollision %v", err, tc.refused)
			}
			if tc.refused && (conn != nil || len(p.Names()) != 0) {
				t.Fatalf("a refused connection must be torn down, not pooled: %v", p.Names())
			}
		})
	}
}

func TestToolNamesAreQualifiedByServer(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  ServerConfig
		want []string
	}{
		{"by default server__tool", ServerConfig{Name: "github"}, []string{"github__boom", "github__echo"}},
		{"a configured prefix", ServerConfig{Name: "github", ToolPrefix: "gh."}, []string{"gh.boom", "gh.echo"}},
		{"no prefix when disabled", ServerConfig{Name: "raw", DisablePrefix: true}, []string{"boom", "echo"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tools, err := poolOf(t, nil, tc.cfg).Tools(context.Background(), nil)
			must(t, err)
			var names []string
			for _, tl := range tools {
				names = append(names, tl.Name)
				if bare, ok := SplitQualified(tc.cfg, tl.Name); !ok || QualifiedName(tc.cfg, bare) != tl.Name {
					t.Fatalf("SplitQualified(%q) = %q, %v; not the inverse", tl.Name, bare, ok)
				}
			}
			if !reflect.DeepEqual(names, tc.want) {
				t.Fatalf("names = %v, want %v", names, tc.want)
			}
		})
	}
	if _, ok := SplitQualified(ServerConfig{Name: "a"}, "b__echo"); ok {
		t.Fatal("another server's name split as this one's")
	}
}

// Tools is the backstop for the native tools a host registers after connecting
// and for two servers exposing the same qualified name.
func TestToolsRefusesANameCollision(t *testing.T) {
	raw := func(n string) ServerConfig { return ServerConfig{Name: n, DisablePrefix: true} }
	for _, tc := range []struct {
		name     string
		pool     *Pool
		native   []string
		existing []core.Tool
		owner    string
	}{
		{"a native tool passed in", poolOf(t, nil, raw("srv")), nil, []core.Tool{{Name: "echo"}}, "a native tool"},
		{"a declared native tool", poolOf(t, nil, raw("srv")), []string{"boom"}, nil, "a native tool"},
		{"two servers", poolOf(t, nil, raw("a"), raw("b")), nil, nil, `server "a"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.pool.NativeTools = tc.native
			_, err := tc.pool.Tools(context.Background(), tc.existing)
			if !errors.Is(err, ErrNameCollision) || !strings.Contains(err.Error(), tc.owner) {
				t.Fatalf("err = %v, want ErrNameCollision naming %s", err, tc.owner)
			}
		})
	}
}

func TestToolsFailsWhenAServerCannotList(t *testing.T) {
	p := poolOf(t, nil, ServerConfig{Name: "gone"})
	conn, _ := p.Get("gone")
	must(t, conn.Close())
	if _, err := p.Tools(context.Background(), nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("err = %v, want the server's listing error", err)
	}
}

func TestThePoolKeysConnectionsByNameInConnectionOrder(t *testing.T) {
	p := poolOf(t, nil, ServerConfig{Name: "b"}, ServerConfig{Name: "a"})
	again, _ := pair(t, echoServer(), ServerConfig{Name: "a"}, ConnectionOptions{})
	if err := p.Add(again); err == nil {
		t.Fatal("a second server named a must be refused")
	}
	if got := p.Names(); !slices.Equal(got, []string{"b", "a"}) {
		t.Fatalf("names = %v, want connection order", got)
	}
	a, ok := p.Get("a")
	must(t, p.Close())
	if !ok || a.Alive() || len(p.Names()) != 0 {
		t.Fatalf("after Close: alive %v, names %v; want every connection closed", a.Alive(), p.Names())
	}
	if _, ok := p.Get("a"); ok {
		t.Fatal("a closed pool still hands out connections")
	}
}

// A server that answers and exits at once still has its answer read; with a
// negative reconnect limit it is then never re-spawned.
func TestAStdioServersLastAnswerIsReadAndANegativeLimitNeverRespawns(t *testing.T) {
	p := NewPool(ConnectionOptions{})
	t.Cleanup(func() { _ = p.Close() })
	conn, err := spawn(t, p, "one-shot", ServerConfig{Name: "one-shot", PerSessionReconnectLimit: -1}, nil, nil)
	if err != nil {
		t.Fatalf("the answer written just before exit was lost: %v", err)
	}
	testutil.Eventually(t, "the client to notice the exit", func() bool { return !conn.Alive() })
	if _, err := conn.Session(context.Background()); !errors.Is(err, ErrReconnectLimit) || conn.Reconnects() != 0 {
		t.Fatalf("err = %v, reconnects %d; want ErrReconnectLimit with no respawn", err, conn.Reconnects())
	}
}

// A disconnect mid-call is an error result and the next call re-spawns the
// server; past the reconnect limit the connection is dead and every call fails
// fast without re-spawning.
func TestADeadStdioServerIsRespawnedWithinTheReconnectLimit(t *testing.T) {
	var w warnings
	p := NewPool(ConnectionOptions{Warnf: w.warnf})
	t.Cleanup(func() { _ = p.Close() })
	conn, err := spawn(t, p, "serve", ServerConfig{Name: "mortal", PerSessionReconnectLimit: 1}, nil, nil)
	must(t, err)
	ctx := context.Background()
	tools := toolsOf(t, p)
	call := func(name string) core.ToolResult { return tools["mortal__"+name].Execute(ctx, nil) }

	if r := call("crash"); r.OK {
		t.Fatal("a call cut off by the server's exit must be an error result")
	}
	testutil.Eventually(t, "the client to notice the exit", func() bool { return !conn.Alive() })
	if r := call("hello"); !r.OK || conn.Reconnects() != 1 || !conn.Alive() {
		t.Fatalf("the call after a death must re-spawn the server: %+v, reconnects %d", r, conn.Reconnects())
	}
	if !w.has(`server "mortal" died; reconnecting (1/1)`) {
		t.Fatalf("the reconnect was not reported: %q", w.list)
	}

	if r := call("die"); !r.OK {
		t.Fatalf("die must answer before exiting: %+v", r)
	}
	testutil.Eventually(t, "the client to notice the second exit", func() bool { return !conn.Alive() })
	for range 2 {
		r := call("hello")
		if r.OK || !strings.Contains(r.Detail, "reconnect limit") || !conn.Dead() || conn.Reconnects() != 1 {
			t.Fatalf("past the limit: %+v, dead %v, reconnects %d; want a fast failure, no respawn",
				r, conn.Dead(), conn.Reconnects())
		}
	}
}

// Every reconnect attempt counts against the budget, a failed spawn included;
// zero means the default limit.
func TestAFailedRespawnSpendsTheReconnectBudget(t *testing.T) {
	dir := t.TempDir() + "/cwd"
	must(t, os.Mkdir(dir, 0o755))
	p := NewPool(ConnectionOptions{})
	t.Cleanup(func() { _ = p.Close() })
	conn, err := spawn(t, p, "serve", ServerConfig{Name: "mortal", Dir: dir}, nil, nil)
	must(t, err)
	ctx := context.Background()
	if _, err := conn.Call(ctx, "crash", nil); err == nil {
		t.Fatal("a call cut off by the server's exit must fail")
	}
	testutil.Eventually(t, "the client to notice the exit", func() bool { return !conn.Alive() })
	must(t, os.Remove(dir)) // the next spawn cannot start

	for i := 1; i <= DefaultReconnectLimit; i++ {
		if _, err := conn.Call(ctx, "hello", nil); err == nil || conn.Reconnects() != i {
			t.Fatalf("attempt %d: err = %v, reconnects %d", i, err, conn.Reconnects())
		}
	}
	if _, err := conn.Call(ctx, "hello", nil); !errors.Is(err, ErrReconnectLimit) || !conn.Dead() {
		t.Fatalf("err = %v; want ErrReconnectLimit after %d failed attempts", err, DefaultReconnectLimit)
	}
}
