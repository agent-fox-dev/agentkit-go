package landlock

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// TS-18-23: the marker records the owner's pid, start time and boot id, its
// role, the creation time, TTL, network mode and labels, and never
// Spec.Env. 18-REQ-4.2.
func TestMarkerContents_TS18_23(t *testing.T) {
	f := newInProc(t)
	var copied []byte
	f.before = func(req startReq) {
		copied, _ = os.ReadFile(filepath.Join(filepath.Dir(argAfter(req.args, "--workdir")), "agentkit-sandbox.json"))
	}
	p := newTestProvider(t, Options{}, f.with)
	before := time.Now()
	s, err := p.Create(context.Background(), sandbox.Spec{Env: map[string]string{"SECRET_TOKEN": "env-sentinel-91f3"},
		Labels: map[string]string{"a": "b"}, TTL: 2 * time.Minute, Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	after := time.Now()
	final, err := os.ReadFile(filepath.Join(p.root, s.ID(), "agentkit-sandbox.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range [][]byte{copied, final} {
		var m marker
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("marker %q: %v", b, err)
		}
		if m.TTL != 2*time.Minute || m.Network != "unrestricted" || m.Labels["a"] != "b" || len(m.Labels) != 1 || m.Created.Before(before.Add(-time.Second)) || m.Created.After(after) {
			t.Errorf("marker %+v", m)
		}
		if start, _, _ := f.table.lookup(m.PID); m.StartTime != start || m.BootID != "B1" {
			t.Errorf("marker owner %d start %d boot %q; the table says %d, B1", m.PID, m.StartTime, m.BootID, start)
		}
		if bytes.Contains(b, []byte("env-sentinel-91f3")) || bytes.Contains(b, []byte("SECRET_TOKEN")) {
			t.Errorf("the marker carries Spec.Env: %s", b)
		}
	}
}

// TS-18-24: the marker names the creator until the agent starts and is then
// atomically replaced to name the agent. 18-REQ-4.3.
func TestMarkerReplaced_TS18_24(t *testing.T) {
	f := newInProc(t)
	var seen marker
	f.before = func(req startReq) {
		m, err := readMarker(filepath.Dir(argAfter(req.args, "--workdir")))
		if err != nil {
			t.Error(err)
		}
		seen = m
	}
	f.pid = func() int { return 4242 }
	p := newTestProvider(t, Options{}, f.with)
	s, err := p.Create(context.Background(), sandbox.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	if seen.PID != os.Getpid() || seen.Role != "creator" || seen.StartTime != creatorStart {
		t.Errorf("the marker before the start: %+v", seen)
	}
	m, err := readMarker(filepath.Join(p.root, s.ID()))
	if err != nil || m.PID != 4242 || m.StartTime != 42420 || m.Role != "agent" {
		t.Errorf("the marker after Create: %+v, %v", m, err)
	}
	if got := entries(t, filepath.Join(p.root, s.ID())); !slices.Equal(got, []string{"agentkit-sandbox.json", "home", "tmp", "workspace"}) {
		t.Errorf("the sandbox directory holds %v", got)
	}
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(s.ID()) {
		t.Errorf("ID %q", s.ID())
	}
}
