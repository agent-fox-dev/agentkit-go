package gatewayd

import (
	"context"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
)

type closeCounter struct {
	sandbox.Sandbox
	closes int
}

func (c *closeCounter) Close(context.Context) error { c.closes++; return nil }

// A removal holding an entry already removed, as a Destroy that loses the race does, closes nothing more.
func TestRemoveOfAnEntryAlreadyRemovedClosesNothing(t *testing.T) {
	s, err := New(Config{Backend: local.Unisolated(), NetworkModes: []sandbox.NetworkMode{sandbox.NetworkUnrestricted},
		Tokens: []string{"0123456789abcdefghijklmnopqrstuvwxyz"}})
	if err != nil {
		t.Fatal(err)
	}
	sb := &closeCounter{}
	e := &entry{id: "sb-1", owner: "p", expires: time.Now().Add(time.Hour), sb: sb}
	if err := s.reg.reserve(); err != nil || s.reg.add(e) != nil {
		t.Fatalf("reserve and add: %v", err)
	}
	for range 3 {
		if err := s.reg.remove(context.Background(), e, reasonEmbedder); err != nil {
			t.Fatal(err)
		}
	}
	if sb.closes != 1 || s.reg.get(e.id) != nil {
		t.Errorf("%d Close calls and the entry held: %v; want 1 and gone", sb.closes, s.reg.get(e.id) != nil)
	}
}
