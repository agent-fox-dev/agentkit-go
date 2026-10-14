package gateway

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// countingDialer counts its calls and never connects.
type countingDialer struct{ n atomic.Int32 }

func (d *countingDialer) Dial(context.Context, string) (net.Conn, error) {
	d.n.Add(1)
	return nil, errors.New("countingDialer never connects")
}

// eventually fails t unless cond holds within 5 s.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TS-19-8: Dial applies the default ports, TLS for https and plaintext for
// loopback http, through Options.Dialer. 19-REQ-2.1.
func TestDialPortsAndTransport_TS19_8(t *testing.T) {
	pki := newTestPKI(t)
	for _, c := range []struct {
		url, want string
		tls       bool
	}{
		{"http://127.0.0.1", "127.0.0.1:80", false},
		{"http://127.0.0.1/", "127.0.0.1:80", false},
		{"http://localhost:9000", "localhost:9000", false},
		{"http://[::1]", "[::1]:80", false},
		{"https://gw.example.test", "gw.example.test:443", true},
	} {
		t.Run(c.url, func(t *testing.T) {
			var srvTLS *tls.Config
			var cliTLS *tls.Config
			if c.tls {
				srvTLS, cliTLS = pki.server, &tls.Config{RootCAs: pki.pool}
			}
			f := newFakeGW(t, srvTLS)
			p, err := Dial(context.Background(), c.url, Options{Token: testToken, Dialer: f.Dial, TLSConfig: cliTLS})
			if err != nil || p == nil {
				t.Fatalf("Dial(%s) = %v, %v", c.url, p, err)
			}
			defer p.Close()
			if got := f.dialled(); !slices.Equal(got, []string{c.want}) {
				t.Errorf("the Dialer received %v, want [%s]", got, c.want)
			}
		})
	}
	// A plaintext client against the TLS server fails: the https case above
	// really completed a TLS handshake.
	f := newFakeGW(t, pki.server)
	if _, err := Dial(context.Background(), "http://127.0.0.1", Options{Token: testToken, Dialer: f.Dial}); err == nil {
		t.Error("a plaintext Dial to a TLS gateway succeeded")
	}
}

// TS-19-9: a URL with user information, a query, a fragment, a path, a
// foreign scheme or a non-loopback http host is refused without connecting.
// 19-REQ-2.2.
func TestDialRefusesURL_TS19_9(t *testing.T) {
	var d countingDialer
	for _, c := range []struct{ url, word string }{
		{"http://user:pw@127.0.0.1", "user information"},
		{"https://gw.example.test/x", "path"},
		{"https://gw.example.test?q=1", "query"},
		{"https://gw.example.test#f", "fragment"},
		{"ftp://gw.example.test", "scheme"},
		{"grpc://127.0.0.1", "scheme"},
		{"http://gw.example.test", "loopback"},
		{"http://10.0.0.1", "loopback"},
	} {
		p, err := Dial(context.Background(), c.url, Options{Token: testToken, Dialer: d.Dial})
		if p != nil || err == nil {
			t.Errorf("Dial(%s) = %v, %v; want a refusal", c.url, p, err)
			continue
		}
		if !strings.Contains(strings.ToLower(err.Error()), c.word) {
			t.Errorf("Dial(%s): %q does not name %q", c.url, err, c.word)
		}
	}
	if n := d.n.Load(); n != 0 {
		t.Errorf("the Dialer was called %d times, want 0", n)
	}
}

// TS-19-10: an empty token, or one with whitespace or characters outside
// printable ASCII, is refused without connecting and without echoing it.
// 19-REQ-2.3.
func TestDialRefusesToken_TS19_10(t *testing.T) {
	var d countingDialer
	for _, tok := range []string{"", "has space", "tab\tin", "ctl\x01char", "ünïcode-token", "trailing\n"} {
		p, err := Dial(context.Background(), "http://127.0.0.1", Options{Token: tok, Dialer: d.Dial})
		if p != nil || err == nil {
			t.Errorf("Dial with token %q = %v, %v; want a refusal", tok, p, err)
			continue
		}
		if !strings.Contains(strings.ToLower(err.Error()), "token") {
			t.Errorf("token %q: %q does not name the token rule", tok, err)
		}
		if tok != "" && strings.Contains(err.Error(), tok) {
			t.Errorf("the error %q echoes the token", err)
		}
	}
	if n := d.n.Load(); n != 0 {
		t.Errorf("the Dialer was called %d times, want 0", n)
	}
}

// TS-19-12: a successful handshake yields a Provider whose Gateway() reports
// the answered identity and limits; a newer minor is accepted. 19-REQ-2.5.
func TestDialGatewayInfo_TS19_12(t *testing.T) {
	f := newFakeGW(t, nil)
	f.set(func() {
		f.caps.ProtocolMinor, f.caps.Version, f.caps.Provider = 3, "v0.19.0", "podman"
		f.caps.MaxTtlNanos, f.caps.MaxSandboxes = int64(2*time.Hour), 5
	})
	p := f.dial(t)
	want := GatewayInfo{URL: "http://127.0.0.1", Version: "v0.19.0", Backend: "podman", ProtocolMajor: 1, ProtocolMinor: 3,
		MaxTTL: 2 * time.Hour, MaxSandboxes: 5}
	if got := p.Gateway(); got != want {
		t.Fatalf("Gateway() = %+v, want %+v", got, want)
	}
}

// TS-19-13: a refused, incompatible or failing handshake closes the
// connection and returns the matching error. 19-REQ-2.6, 19-REQ-2.7,
// 19-REQ-2.8.
func TestDialHandshakeFailures_TS19_13(t *testing.T) {
	const url = "http://127.0.0.1"
	for _, c := range []struct {
		name  string
		setup func(f *fakeGW)
		ctx   time.Duration
		check func(t *testing.T, err error)
	}{
		{"unauthenticated", func(f *fakeGW) { f.capsErr = status.Error(codes.Unauthenticated, "no") }, 0, func(t *testing.T, err error) {
			if !errors.Is(err, ErrUnauthenticated) {
				t.Errorf("%v does not match ErrUnauthenticated", err)
			}
		}},
		{"major", func(f *fakeGW) { f.caps.ProtocolMajor = 2 }, 0, func(t *testing.T, err error) {
			if !errors.Is(err, sandbox.ErrProtocol) || !strings.Contains(err.Error(), "2.") || !strings.Contains(err.Error(), "1.0") {
				t.Errorf("%v: want ErrProtocol naming 2.x and 1.0", err)
			}
		}},
		{"unavailable", func(f *fakeGW) { f.capsErr = status.Error(codes.Unavailable, "down") }, 0, func(t *testing.T, err error) {
			if !strings.Contains(err.Error(), url) {
				t.Errorf("%v does not name %s", err, url)
			}
		}},
		{"deadline", func(f *fakeGW) { f.capsBlock = true }, 100 * time.Millisecond, func(t *testing.T, err error) {
			if !strings.Contains(err.Error(), url) {
				t.Errorf("%v does not name %s", err, url)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeGW(t, nil)
			f.set(func() { c.setup(f) })
			ctx := context.Background()
			if c.ctx > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, c.ctx)
				defer cancel()
			}
			p, err := Dial(ctx, url, Options{Token: testToken, Dialer: f.Dial})
			if p != nil || err == nil {
				t.Fatalf("Dial = %v, %v; want a failure", p, err)
			}
			c.check(t, err)
			eventually(t, "the client connection to close", func() bool { return f.openConns() == 0 })
		})
	}
}
