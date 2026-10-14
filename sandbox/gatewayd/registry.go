package gatewayd

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/attach"
)

// entry is one sandbox the gateway holds.
type entry struct {
	id      string
	owner   owner
	created time.Time
	expires time.Time
	spec    sandbox.Spec
	sb      sandbox.Sandbox
	slot    slot
	// timer removes the entry at its expiry.
	timer *time.Timer
}

// registry is the gateway's sandboxes by id, the creates in progress, and
// whether Shutdown has closed it.
type registry struct {
	s       *Server
	mu      sync.Mutex
	entries map[string]*entry
	pending int
	closed  bool
}

// errFull is reserve's refusal when the gateway holds as many sandboxes as
// it allows.
var errFull = errors.New("gatewayd: at capacity")

func newRegistry(s *Server) *registry { return &registry{s: s, entries: map[string]*entry{}} }

// reserve counts a create in progress, after pruning entries whose backend
// sandbox is no longer live. It refuses with errFull when recorded entries
// plus creates in progress reach the maximum, and with Unavailable once the
// registry is closed. Each successful reserve is ended by add, which turns
// it into an entry, or by unreserve.
func (r *registry) reserve() error {
	r.prune()
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case r.closed:
		return unavailable("gatewayd: the gateway is shutting down")
	case len(r.entries)+r.pending >= r.s.maxSandboxes:
		return errFull
	}
	r.pending++
	return nil
}

// unreserve ends a create in progress.
func (r *registry) unreserve() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pending--
}

// prune removes every entry whose backend sandbox is no longer live.
func (r *registry) prune() {
	r.mu.Lock()
	var dead []*entry
	for _, e := range r.entries {
		if attach.Live(e.sb) != nil {
			dead = append(dead, e)
		}
	}
	r.mu.Unlock()
	for _, e := range dead {
		_ = r.remove(e, reasonUnavailable)
	}
}

// add turns a reservation into the entry e and arms its expiry, or refuses
// once the registry is closed, leaving the reservation to unreserve.
func (r *registry) add(e *entry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return unavailable("gatewayd: the gateway is shutting down")
	}
	r.pending--
	r.entries[e.id] = e
	e.timer = time.AfterFunc(time.Until(e.expires), func() { _ = r.remove(e, reasonExpired) })
	return nil
}

// lookup is o's sandbox id, or nil: another owner's id is nil too.
func (r *registry) lookup(o owner, id string) *entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e := r.entries[id]; e != nil && e.owner == o {
		return e
	}
	return nil
}

// access is o's live sandbox id. An unknown id and another owner's are
// answered Unavailable alike; a sandbox whose backend is no longer live is
// removed and answered Unavailable too.
func (r *registry) access(o owner, id string) (*entry, error) {
	e := r.lookup(o, id)
	if e == nil {
		return nil, unavailable("gatewayd: no sandbox %q", id)
	}
	if err := attach.Live(e.sb); err != nil {
		_ = r.remove(e, reasonUnavailable)
		return nil, unavailable("gatewayd: sandbox %q is no longer available: %v", id, err)
	}
	return e, nil
}

// list is o's live sandboxes, by creation time and then id; the ones no
// longer live are removed.
func (r *registry) list(o owner) []*entry {
	r.mu.Lock()
	var mine []*entry
	for _, e := range r.entries {
		if e.owner == o {
			mine = append(mine, e)
		}
	}
	r.mu.Unlock()
	var out []*entry
	for _, e := range mine {
		if attach.Live(e.sb) != nil {
			_ = r.remove(e, reasonUnavailable)
			continue
		}
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b *entry) int { return cmp.Or(a.created.Compare(b.created), cmp.Compare(a.id, b.id)) })
	return out
}

// remove deletes e once, stops its expiry, closes its backend sandbox and
// logs the destroy with reason; it returns the Close error. A later remove
// of the same entry does nothing.
func (r *registry) remove(e *entry, reason string) error {
	r.mu.Lock()
	if r.entries[e.id] != e {
		r.mu.Unlock()
		return nil
	}
	delete(r.entries, e.id)
	if e.timer != nil {
		e.timer.Stop()
	}
	r.mu.Unlock()
	err := e.sb.Close(context.Background())
	r.s.logDestroy(e.id, reason, err)
	return err
}

// closeAll closes the registry to new entries and removes every entry with
// reason; it returns the joined Close errors.
func (r *registry) closeAll(reason string) error {
	r.mu.Lock()
	r.closed = true
	all := slices.Collect(maps.Values(r.entries))
	r.mu.Unlock()
	var errs []error
	for _, e := range all {
		errs = append(errs, r.remove(e, reason))
	}
	return errors.Join(errs...)
}
