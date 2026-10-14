package gatewayd_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
)

// tok40 is a valid 40-character token.
const tok40 = "0123456789abcdefghijklmnopqrstuvwxyzABCD"

// TS-19-30: New refuses a missing backend, bad or repeated tokens, negative
// limits and an empty network intersection. 19-REQ-5.1.
func TestNewRefuses_TS19_30(t *testing.T) {
	be := local.Unisolated() // offers only NetworkUnrestricted
	unr := []sandbox.NetworkMode{sandbox.NetworkUnrestricted}
	for name, cfg := range map[string]gatewayd.Config{
		"nil backend":     {Tokens: []string{tok40}, NetworkModes: unr},
		"no tokens":       {Backend: be, NetworkModes: unr},
		"31 characters":   {Backend: be, Tokens: []string{tok40[:31]}, NetworkModes: unr},
		"space":           {Backend: be, Tokens: []string{tok40[:20] + " " + tok40[:20]}, NetworkModes: unr},
		"control":         {Backend: be, Tokens: []string{tok40[:20] + "\x01" + tok40[:20]}, NetworkModes: unr},
		"repeated":        {Backend: be, Tokens: []string{tok40, tok40}, NetworkModes: unr},
		"MaxSandboxes -1": {Backend: be, Tokens: []string{tok40}, NetworkModes: unr, MaxSandboxes: -1},
		"MaxTTL -1":       {Backend: be, Tokens: []string{tok40}, NetworkModes: unr, MaxTTL: -1},
		"ReapInterval -1": {Backend: be, Tokens: []string{tok40}, NetworkModes: unr, ReapInterval: -1},
		"no common mode":  {Backend: be, Tokens: []string{tok40}, NetworkModes: []sandbox.NetworkMode{sandbox.NetworkNone}},
	} {
		s, err := gatewayd.New(cfg)
		if s != nil || err == nil {
			t.Errorf("%s: New = %v, %v; want a refusal", name, s, err)
			continue
		}
		if strings.Contains(err.Error(), tok40[:20]) {
			t.Errorf("%s: the error %q contains the token", name, err)
		}
	}
	_, err := gatewayd.New(gatewayd.Config{Backend: be, Tokens: []string{tok40}, NetworkModes: []sandbox.NetworkMode{sandbox.NetworkNone}})
	if err == nil || !strings.Contains(err.Error(), "none") || !strings.Contains(err.Error(), "unrestricted") {
		t.Errorf("the network refusal %v does not name both [none] and [unrestricted]", err)
	}
}

// TS-19-31: zero optional fields default to a none-only ceiling, 16
// sandboxes, a 24h TTL, a 10m reap interval and a silent logger.
// 19-REQ-5.2.
func TestDefaults_TS19_31(t *testing.T) {
	be := gwtest.NewCounting()
	be.SetCaps(sandbox.Capabilities{Provider: "stub", Network: []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkUnrestricted}})
	env := gwtest.Start(t, gatewayd.Config{Backend: be, Tokens: []string{gwtest.Token}})
	p := env.Dial(t, gwtest.Token)
	if n := p.Capabilities().Network; len(n) != 1 || n[0] != sandbox.NetworkNone {
		t.Errorf("Capabilities().Network = %v, want [none]", n)
	}
	if g := p.Gateway(); g.MaxSandboxes != 16 || g.MaxTTL != 24*time.Hour {
		t.Errorf("Gateway() = %+v, want 16 sandboxes and 24h", g)
	}
	time.Sleep(300 * time.Millisecond)
	if n := be.Reaps(); n != 1 {
		t.Errorf("the backend was reaped %d times in 300 ms, want 1 (a 10m interval)", n)
	}
}

// TS-19-14: capabilities over a local backend are named gateway/local,
// limited to the ceiling, pass the backend's fields through and are served
// from the cache. 19-REQ-3.1.
func TestCapabilitiesOverLocal_TS19_14(t *testing.T) {
	env := gwtest.Start(t, gatewayd.Config{Backend: local.Unisolated(), NetworkModes: []sandbox.NetworkMode{sandbox.NetworkUnrestricted},
		MaxTTL: 2 * time.Hour, MaxSandboxes: 3})
	p := env.Dial(t, gwtest.Token)
	c, lc := p.Capabilities(), local.Unisolated().Capabilities()
	if c.Provider != "gateway/local" || len(c.Network) != 1 || c.Network[0] != sandbox.NetworkUnrestricted {
		t.Errorf("Provider %q Network %v, want gateway/local [unrestricted]", c.Provider, c.Network)
	}
	if c.Isolation != lc.Isolation || c.Resources != lc.Resources || c.Image != lc.Image || c.Workdir != lc.Workdir || c.Landlock != lc.Landlock {
		t.Errorf("capabilities %+v do not pass local's %+v through", c, lc)
	}
	if !strings.HasPrefix(c.Notes, lc.Notes) {
		t.Errorf("Notes %q does not start with local's %q", c.Notes, lc.Notes)
	}
	sentences := strings.Split(strings.TrimSuffix(c.Notes, "."), ". ")
	last := sentences[len(sentences)-1]
	for _, want := range []string{"127.0.0.1", "2h", "3"} {
		if !strings.Contains(last, want) {
			t.Errorf("the last sentence %q does not name %s", last, want)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := env.Server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if again := p.Capabilities(); again.Provider != c.Provider || again.Notes != c.Notes || len(again.Network) != len(c.Network) {
		t.Errorf("Capabilities() changed with the server gone: %+v, was %+v", again, c)
	}
}
