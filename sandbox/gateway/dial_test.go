package gateway

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// Dial reaches the default port, over TLS for https, verifying the gateway
// against a clone of the caller's TLS config, or in plaintext for loopback
// http, and carries messages up to MaxMessageBytes.
func TestDialUsesTheDefaultPortsTLSForHTTPSAndPlaintextForLoopbackHTTP(t *testing.T) {
	pool, serverTLS := testTLS(t)
	for _, c := range []struct {
		url, addr string
		tls       bool
	}{
		{"http://127.0.0.1", "127.0.0.1:80", false},
		{"http://127.0.0.1/", "127.0.0.1:80", false},
		{"http://localhost:9000", "localhost:9000", false},
		{"http://[::1]", "[::1]:80", false},
		{"https://example.com", "example.com:443", true},
	} {
		var srv, cli *tls.Config
		if c.tls {
			srv, cli = serverTLS, &tls.Config{RootCAs: pool}
		}
		snap := cli.Clone()
		f := newFakeGW(t, srv)
		p, err := Dial(context.Background(), c.url, Options{Token: testToken, Dialer: f.Dial, TLSConfig: cli})
		if err != nil {
			t.Errorf("Dial(%s): %v", c.url, err)
			continue
		}
		in, _ := json.Marshal(map[string]any{"path": "big.bin", "content": strings.Repeat("x", 12<<20)})
		_ = tool(t, mustCreate(t, p), "write_file").Execute(context.Background(), in)
		if got := f.agent.LastInput(); !bytes.Equal(got, in) || !reflect.DeepEqual(cli, snap) {
			t.Errorf("Dial(%s): the agent received %d of %d input bytes; the TLS config is unchanged %v", c.url, len(got), len(in), reflect.DeepEqual(cli, snap))
		}
		f.with(func() {
			if !slices.Equal(f.dials, []string{c.addr}) {
				t.Errorf("Dial(%s) dialled %v, want [%s]", c.url, f.dials, c.addr)
			}
		})
		if c.tls {
			if _, err := Dial(context.Background(), c.url, Options{Token: testToken, Dialer: f.Dial}); err == nil || !strings.Contains(err.Error(), c.url) {
				t.Errorf("a Dial trusting only the system roots gave %v, want a failure naming %s", err, c.url)
			}
		}
	}
}

func TestDialRefusesABadURLOrTokenWithoutConnecting(t *testing.T) {
	dialled := 0
	never := Options{Token: testToken, Dialer: func(context.Context, string) (net.Conn, error) { dialled++; return nil, errors.New("no") }}
	for _, c := range []struct{ url, word string }{
		{"http://[::1", "parse"},
		{"ftp://gw.example.test", "scheme"},
		{"http:gw.example.test", "opaque"},
		{"http://user:pw@127.0.0.1", "user information"},
		{"https://gw.example.test?q=1", "query"},
		{"https://gw.example.test?", "query"},
		{"https://gw.example.test#f", "fragment"},
		{"https://gw.example.test/x", "path"},
		{"https:///", "no host"},
		{"http://gw.example.test", "loopback"},
		{"http://10.0.0.1", "loopback"},
	} {
		if p, err := Dial(context.Background(), c.url, never); p != nil || err == nil || !strings.Contains(err.Error(), c.word) {
			t.Errorf("Dial(%s) = %v, %v; want a refusal naming %q", c.url, p, err, c.word)
		}
	}
	for _, tok := range []string{"", "has space", "tab\tin", "ctl\x01char", "ünïcode-token", "trailing\n"} {
		never.Token = tok
		p, err := Dial(context.Background(), "http://127.0.0.1", never)
		if p != nil || err == nil || !strings.Contains(err.Error(), "Options.Token") || (tok != "" && strings.Contains(err.Error(), tok)) {
			t.Errorf("token %q: %v, %v; want a refusal naming the rule and not echoing the token", tok, p, err)
		}
	}
	if dialled != 0 {
		t.Errorf("the Dialer was called %d times, want 0", dialled)
	}
}

func TestDialReportsTheGatewayAndItsCapabilities(t *testing.T) {
	for _, notes := range []string{"podman notes.", ""} {
		f := newFakeGW(t, nil)
		f.with(func() {
			f.caps.ProtocolMinor, f.caps.Version, f.caps.MaxTtlNanos, f.caps.MaxSandboxes, f.caps.Notes = 3, "v0.19.0", int64(2*time.Hour), 5, notes
		})
		p := f.dial(t)
		want := GatewayInfo{URL: "http://127.0.0.1", Version: "v0.19.0", Backend: "podman", ProtocolMajor: 1, ProtocolMinor: 3, MaxTTL: 2 * time.Hour, MaxSandboxes: 5}
		if got := p.Gateway(); got != want {
			t.Errorf("Gateway() = %+v, want %+v", got, want)
		}
		c := p.Capabilities()
		limits := "Sandboxes run on the gateway at 127.0.0.1, which accepts a TTL of at most 2h0m0s and holds at most 5 sandboxes at once."
		wantNotes := "podman notes. " + limits
		if notes == "" {
			wantNotes = limits
		}
		if c.Notes != wantNotes {
			t.Errorf("Notes %q, want %q", c.Notes, wantNotes)
		}
		if c.Provider != "gateway/podman" || c.Isolation != sandbox.IsolationContainer || !c.Image || !slices.Equal(c.Network, []sandbox.NetworkMode{sandbox.NetworkNone}) {
			t.Errorf("Capabilities() = %+v", c)
		}
	}
}

func TestDialFailsAndClosesTheConnectionWhenTheHandshakeFails(t *testing.T) {
	for _, c := range []struct {
		name     string
		setup    func(f *fakeGW)
		sentinel error
		text     string
	}{
		{"unauthenticated", func(f *fakeGW) { f.capsErr = status.Error(codes.Unauthenticated, "no") }, ErrUnauthenticated, "http://127.0.0.1"},
		{"another major", func(f *fakeGW) { f.caps.ProtocolMajor = 2 }, sandbox.ErrProtocol, "protocol 2.0, this client 1.0"},
		{"unavailable", func(f *fakeGW) { f.capsErr = status.Error(codes.Unavailable, "down") }, nil, "http://127.0.0.1"},
	} {
		f := newFakeGW(t, nil)
		f.with(func() { c.setup(f) })
		p, err := Dial(context.Background(), "http://127.0.0.1", Options{Token: testToken, Dialer: f.Dial})
		if p != nil || err == nil || (c.sentinel != nil && !errors.Is(err, c.sentinel)) || !strings.Contains(err.Error(), c.text) {
			t.Errorf("%s: Dial = %v, %v; want %v naming %q", c.name, p, err, c.sentinel, c.text)
		}
		testutil.Eventually(t, c.name+": the connection to close", f.connsClosed)
	}
}
