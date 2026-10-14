package gatewayd

import (
	"cmp"
	"context"
	"slices"
	"sync"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// entry is one sandbox the gateway holds.
type entry struct {
	id      string
	owner   owner
	created time.Time
	expires time.Time
	spec    sandbox.Spec
	sb      sandbox.Sandbox
}

// registry is the gateway's sandboxes by id.
type registry struct {
	s       *Server
	mu      sync.Mutex
	entries map[string]*entry
}

func newRegistry(s *Server) *registry { return &registry{s: s, entries: map[string]*entry{}} }

// add records e.
func (r *registry) add(e *entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[e.id] = e
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

// list is o's sandboxes, by creation time and then id.
func (r *registry) list(o owner) []*entry {
	r.mu.Lock()
	var out []*entry
	for _, e := range r.entries {
		if e.owner == o {
			out = append(out, e)
		}
	}
	r.mu.Unlock()
	slices.SortFunc(out, func(a, b *entry) int { return cmp.Or(a.created.Compare(b.created), cmp.Compare(a.id, b.id)) })
	return out
}

// remove deletes e once, closes its backend sandbox and logs the destroy
// with reason; it returns the Close error. A later remove of the same entry
// does nothing.
func (r *registry) remove(e *entry, reason string) error {
	r.mu.Lock()
	if r.entries[e.id] != e {
		r.mu.Unlock()
		return nil
	}
	delete(r.entries, e.id)
	r.mu.Unlock()
	err := e.sb.Close(context.Background())
	r.s.logDestroy(e.id, reason, err)
	return err
}
