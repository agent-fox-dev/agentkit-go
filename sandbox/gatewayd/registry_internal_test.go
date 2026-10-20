package gatewayd

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
)

// closeCounter is a sandbox that only counts its Close calls; the registry's
// removal path calls nothing else.
type closeCounter struct {
	sandbox.Sandbox
	id     string
	closes atomic.Int32
}

func (c *closeCounter) ID() string                  { return c.id }
func (c *closeCounter) Close(context.Context) error { c.closes.Add(1); return nil }

// TS-25-37: a removal that loses the race to another removal of the same
// entry skips the Close and the destroy log, so the backend sandbox is closed
// once in total. 25-REQ-8.5. It is in-package: every caller holds the entry
// it looked up, which an external test cannot arrange, because the window
// between Destroy's lookup and its removal is a few instructions wide.
func TestRemoveLosingTheRaceClosesOnce_TS25_37(t *testing.T) {
	var logs bytes.Buffer
	s, err := New(Config{
		Backend:      local.Unisolated(),
		NetworkModes: []sandbox.NetworkMode{sandbox.NetworkUnrestricted},
		Authenticate: func(context.Context, string) (string, bool) { return "p", true },
		Logger:       slog.New(slog.NewJSONHandler(&lockedWriter{w: &logs}, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	sb := &closeCounter{id: "sb-1"}
	if err := s.reg.reserve(); err != nil {
		t.Fatal(err)
	}
	if err := s.reg.add(&entry{id: sb.id, owner: "p", expires: time.Now().Add(time.Hour), sb: sb}); err != nil {
		t.Fatal(err)
	}
	e := s.reg.get(sb.id) // the entry every remover below has already looked up

	const removers = 16
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range removers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := s.reg.remove(context.Background(), e, reasonEmbedder); err != nil {
				t.Errorf("remove = %v, want nil", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if n := sb.closes.Load(); n != 1 {
		t.Errorf("the backend sandbox was closed %d times, want 1", n)
	}
	if n := strings.Count(logs.String(), `"msg":"sandbox destroyed"`); n != 1 {
		t.Errorf("%d destroy lines, want 1:\n%s", n, logs.String())
	}
	if got := s.reg.get(sb.id); got != nil {
		t.Error("the entry is still registered")
	}
}

// lockedWriter serialises writes from the goroutines of a test.
type lockedWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
