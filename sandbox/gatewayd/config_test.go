package gatewayd_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/buildver"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
)

func TestNewRefusesAnInvalidConfig(t *testing.T) {
	be := local.Unisolated() // offers only NetworkUnrestricted
	acceptAll := func(context.Context, string) (string, bool) { return "p", true }
	toks := []string{tok40}
	for _, c := range []struct {
		name string
		cfg  gatewayd.Config
		want []string // substrings of the error, or its whole text when one starts with "gatewayd:"
	}{
		{"nil backend", gatewayd.Config{Authenticate: acceptAll}, []string{"gatewayd: Config.Backend is nil"}},
		{"both modes", gatewayd.Config{Backend: be, Tokens: toks, Authenticate: acceptAll}, []string{"Tokens", "Authenticate", "both"}},
		{"nil tokens", gatewayd.Config{Backend: be}, []string{"Tokens", "Authenticate", "neither"}},
		{"empty tokens", gatewayd.Config{Backend: be, Tokens: []string{}}, []string{"Tokens", "Authenticate", "neither"}},
		{"short token", gatewayd.Config{Backend: be, Tokens: []string{tok40[:31]}},
			[]string{"gatewayd: Config.Tokens: token 1 is 31 characters long; at least 32 are required"}},
		{"space", gatewayd.Config{Backend: be, Tokens: []string{tok40[:20] + " " + tok40[:20]}}, []string{"Config.Tokens", "offset 20"}},
		{"control", gatewayd.Config{Backend: be, Tokens: []string{tok40[:20] + "\x01" + tok40[:20]}}, []string{"Config.Tokens", "offset 20"}},
		{"repeated", gatewayd.Config{Backend: be, Tokens: []string{tok40, tok40}}, []string{"gatewayd: Config.Tokens: token 2 repeats token 1"}},
		{"MaxSandboxes", gatewayd.Config{Backend: be, Authenticate: acceptAll, MaxSandboxes: -1}, []string{"gatewayd: Config.MaxSandboxes is negative"}},
		{"MaxTTL", gatewayd.Config{Backend: be, Tokens: toks, MaxTTL: -1}, []string{"gatewayd: Config.MaxTTL is negative"}},
		{"ReapInterval", gatewayd.Config{Backend: be, Tokens: toks, ReapInterval: -1}, []string{"gatewayd: Config.ReapInterval is negative"}},
		{"no common mode", gatewayd.Config{Backend: be, Tokens: toks}, []string{"[none]", `"local"`, "[unrestricted]"}},
	} {
		if c.name != "no common mode" && c.cfg.NetworkModes == nil {
			c.cfg.NetworkModes = unrestricted
		}
		s, err := gatewayd.New(c.cfg)
		if s != nil || err == nil {
			t.Errorf("%s: New = %v, %v; want a refusal", c.name, s, err)
			continue
		}
		for _, w := range c.want {
			if strings.HasPrefix(w, "gatewayd:") && err.Error() != w || !strings.Contains(err.Error(), w) {
				t.Errorf("%s: the error is %q, want %q", c.name, err, w)
			}
		}
		if strings.Contains(err.Error(), tok40[:20]) {
			t.Errorf("%s: the error %q contains the token", c.name, err)
		}
	}
}

// The network modes are the backend's within the ceiling (nil meaning [none]); zero limits mean 16 sandboxes and 24h.
func TestCapabilitiesAnswerTheCeilingAndTheLimits(t *testing.T) {
	both := []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkUnrestricted}
	for _, c := range []struct {
		name    string
		cfg     gatewayd.Config
		network []sandboxpb.NetworkMode
		max     int32
		ttl     time.Duration
	}{
		{"defaults", gatewayd.Config{}, []sandboxpb.NetworkMode{sandboxpb.NetworkMode_NONE}, 16, 24 * time.Hour},
		{"set", gatewayd.Config{NetworkModes: both, MaxSandboxes: 3, MaxTTL: 2 * time.Hour},
			[]sandboxpb.NetworkMode{sandboxpb.NetworkMode_NONE, sandboxpb.NetworkMode_UNRESTRICTED}, 3, 2 * time.Hour},
	} {
		be := gwtest.NewCounting()
		be.SetCaps(sandbox.Capabilities{Provider: "stub", Network: both, Image: true, Notes: "stub notes."})
		c.cfg.Backend = be
		r, err := gwtest.Start(t, c.cfg).RawGateway(t).Capabilities(authed(gwtest.Token), &sandboxpb.CapabilitiesRequest{})
		must(t, err)
		if !slices.Equal(r.GetNetwork(), c.network) || r.GetMaxSandboxes() != c.max || time.Duration(r.GetMaxTtlNanos()) != c.ttl {
			t.Errorf("%s: network %v, max %d, TTL %v; want %v, %d, %v", c.name, r.GetNetwork(), r.GetMaxSandboxes(),
				time.Duration(r.GetMaxTtlNanos()), c.network, c.max, c.ttl)
		}
		if r.GetProvider() != "stub" || !r.GetImage() || r.GetNotes() != "stub notes." || r.GetVersion() != buildver.Version() ||
			r.GetProtocolMajor() != gateway.ProtocolMajor || r.GetProtocolMinor() != gateway.ProtocolMinor {
			t.Errorf("%s: %v does not pass the backend's fields through with the gateway's versions", c.name, r)
		}
	}
}
