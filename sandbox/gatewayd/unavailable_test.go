package gatewayd_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/localtest"
)

// conns records every connection its Dial opens, so a test can cut them.
type conns struct {
	dial func(context.Context, string) (net.Conn, error)
	mu   sync.Mutex
	all  []net.Conn
}

func (c *conns) Dial(ctx context.Context, addr string) (net.Conn, error) {
	conn, err := c.dial(ctx, addr)
	if err == nil {
		c.mu.Lock()
		c.all = append(c.all, conn)
		c.mu.Unlock()
	}
	return conn, err
}

// cut closes every connection opened so far.
func (c *conns) cut() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, conn := range c.all {
		_ = conn.Close()
	}
}

// TS-19-70: a cut client connection, a cut backend agent connection or a
// gateway-side destroy fails in-flight and later calls with
// sandbox_unavailable. 19-REQ-10.4.
func TestTransportFailures_TS19_70(t *testing.T) {
	for _, c := range []struct {
		name string
		cut  func(t *testing.T, env *gwtest.Env, be *gwtest.Counting, cs *conns, sb sandbox.Sandbox)
	}{
		{"client connection", func(t *testing.T, _ *gwtest.Env, _ *gwtest.Counting, cs *conns, _ sandbox.Sandbox) { cs.cut() }},
		{"backend agent connection", func(t *testing.T, _ *gwtest.Env, be *gwtest.Counting, _ *conns, sb sandbox.Sandbox) {
			if err := localtest.Cut(be.ByID(sb.ID()).Sandbox); err != nil {
				t.Fatal(err)
			}
		}},
		{"gateway destroy", func(t *testing.T, env *gwtest.Env, _ *gwtest.Counting, _ *conns, sb sandbox.Sandbox) {
			if err := env.Dial(t, gwtest.Token).Destroy(context.Background(), sb.ID()); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			be := gwtest.NewCounting()
			env := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: unrestricted})
			cs := &conns{dial: env.Listener.Dial}
			p, err := gateway.Dial(context.Background(), gwtest.URL, gateway.Options{Token: gwtest.Token, Dialer: cs.Dial})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = p.Close() })
			sb := create(t, p, unrestrictedSpec)
			exec, read := toolOf(t, sb, "execute"), toolOf(t, sb, "read_file")
			res := make(chan string, 1)
			go func() {
				res <- exec.Execute(context.Background(), []byte(`{"command":"touch started; sleep 30"}`)).Error
			}()
			eventually(t, "the command to start", func() bool {
				_, err := os.Stat(filepath.Join(sb.Spec().Workdir, "started"))
				return err == nil
			})
			c.cut(t, env, be, cs, sb)
			select {
			case code := <-res:
				if code != "sandbox_unavailable" {
					t.Errorf("the in-flight call returned %q, want sandbox_unavailable", code)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the in-flight call did not end")
			}
			if r := read.Execute(context.Background(), []byte(`{"path":"started"}`)); r.Error != "sandbox_unavailable" {
				t.Errorf("a later read_file: %+v, want sandbox_unavailable", r)
			}
			if _, err := sb.Sync(context.Background(), sandbox.Tar{R: bytes.NewReader(emptyTar)}); !errors.Is(err, sandbox.ErrUnavailable) {
				t.Errorf("a later Sync: %v, want ErrUnavailable", err)
			}
			if err := sb.Export(context.Background(), ".", sandbox.TarOut{W: &bytes.Buffer{}}); !errors.Is(err, sandbox.ErrUnavailable) {
				t.Errorf("a later Export: %v, want ErrUnavailable", err)
			}
		})
	}
}
