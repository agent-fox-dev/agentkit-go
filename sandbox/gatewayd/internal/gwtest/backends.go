package gwtest

import (
	"context"
	"path/filepath"
	"sync"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
)

// Counting is a backend over another provider (local.Unisolated() by
// default) that counts Create, Reap, sandbox Close and Sync calls and
// records each sandbox it returns. Its Set methods turn it into a stub:
// other capabilities, a Create error, Reap results, a sandbox Close error.
type Counting struct {
	inner sandbox.Provider

	mu        sync.Mutex
	caps      *sandbox.Capabilities
	createErr error
	reaps     []Reaped
	closeErr  error
	creates   int
	reapsN    int
	closes    int
	syncs     int
	sbs       []*Sandbox
}

// Reaped is one Reap result of a stub.
type Reaped struct {
	IDs []string
	Err error
}

// NewCounting is a Counting over local.Unisolated().
func NewCounting() *Counting { return &Counting{inner: local.Unisolated()} }

// SetCaps makes Capabilities return caps. Create still creates local
// sandboxes, with an unrestricted network whatever the spec asks for.
func (c *Counting) SetCaps(caps sandbox.Capabilities) { c.with(func() { c.caps = &caps }) }

// SetCreateErr makes Create return err.
func (c *Counting) SetCreateErr(err error) { c.with(func() { c.createErr = err }) }

// SetReaps queues Reap's results; once they run out Reap returns nothing.
func (c *Counting) SetReaps(r ...Reaped) { c.with(func() { c.reaps = r }) }

// SetCloseErr makes every sandbox's Close return err after closing it.
func (c *Counting) SetCloseErr(err error) { c.with(func() { c.closeErr = err }) }

func (c *Counting) with(fn func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fn()
}

func (c *Counting) Capabilities() sandbox.Capabilities {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.caps != nil {
		return *c.caps
	}
	return c.inner.Capabilities()
}

func (c *Counting) Create(ctx context.Context, spec sandbox.Spec) (sandbox.Sandbox, error) {
	c.mu.Lock()
	c.creates++
	err, stub := c.createErr, c.caps != nil
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if stub {
		spec.Network = sandbox.Network{Mode: sandbox.NetworkUnrestricted}
	}
	sb, err := c.inner.Create(ctx, spec)
	if err != nil {
		return nil, err
	}
	w := &Sandbox{Sandbox: sb, c: c}
	c.with(func() { c.sbs = append(c.sbs, w) })
	return w, nil
}

func (c *Counting) Reap(ctx context.Context) ([]string, error) {
	c.mu.Lock()
	c.reapsN++
	if c.caps != nil || len(c.reaps) > 0 {
		var r Reaped
		if len(c.reaps) > 0 {
			r, c.reaps = c.reaps[0], c.reaps[1:]
		}
		c.mu.Unlock()
		return r.IDs, r.Err
	}
	c.mu.Unlock()
	return c.inner.Reap(ctx)
}

// Creates, Reaps, Closes and Syncs are the call counts.
func (c *Counting) Creates() int { c.mu.Lock(); defer c.mu.Unlock(); return c.creates }
func (c *Counting) Reaps() int   { c.mu.Lock(); defer c.mu.Unlock(); return c.reapsN }
func (c *Counting) Closes() int  { c.mu.Lock(); defer c.mu.Unlock(); return c.closes }
func (c *Counting) Syncs() int   { c.mu.Lock(); defer c.mu.Unlock(); return c.syncs }

// Sandboxes is every sandbox Create returned, in order.
func (c *Counting) Sandboxes() []*Sandbox {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*Sandbox(nil), c.sbs...)
}

// Last is the last sandbox Create returned.
func (c *Counting) Last() *Sandbox {
	sbs := c.Sandboxes()
	return sbs[len(sbs)-1]
}

// ByID is the sandbox with id, or nil.
func (c *Counting) ByID(id string) *Sandbox {
	for _, s := range c.Sandboxes() {
		if s.ID() == id {
			return s
		}
	}
	return nil
}

// Sandbox is a backend sandbox of a Counting: the local sandbox, counted.
type Sandbox struct {
	sandbox.Sandbox
	c *Counting
}

// Unwrap is the local sandbox, for attach's accessors.
func (s *Sandbox) Unwrap() sandbox.Sandbox { return s.Sandbox }

// Dir is the local sandbox's directory: the parent of its workspace.
func (s *Sandbox) Dir() string { return filepath.Dir(s.Spec().Workdir) }

func (s *Sandbox) Close(ctx context.Context) error {
	err := s.Sandbox.Close(ctx)
	s.c.mu.Lock()
	s.c.closes++
	if s.c.closeErr != nil {
		err = s.c.closeErr
	}
	s.c.mu.Unlock()
	return err
}

func (s *Sandbox) Sync(ctx context.Context, src sandbox.Source) (sandbox.SyncResult, error) {
	s.c.with(func() { s.c.syncs++ })
	return s.Sandbox.Sync(ctx, src)
}
