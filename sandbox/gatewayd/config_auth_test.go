package gatewayd_test

import (
	"context"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/token"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
)

// acceptAll is an Authenticator for tests that only build a server.
func acceptAll(context.Context, string) (string, bool) { return "p", true }

// TS-25-1: Authenticator and Config.Authenticate are exported with the
// specified shapes. 25-REQ-1.1.
func TestAuthenticatorShape_TS25_1(t *testing.T) {
	var a gatewayd.Authenticator = func(ctx context.Context, tok string) (string, bool) { return "p", true }
	cfg := gatewayd.Config{Backend: local.Unisolated(), Authenticate: a}
	var f func(context.Context, string) (string, bool) = a
	if p, ok := f(context.Background(), "t"); p != "p" || !ok {
		t.Errorf("the authenticator answered (%q, %v), want (p, true)", p, ok)
	}
	if cfg.Authenticate == nil {
		t.Error("Config.Authenticate is nil after being set")
	}
}

// TS-25-2: New refuses a Config with both Tokens and Authenticate and leaks
// no token. 25-REQ-1.2.
func TestNewRefusesBothModes_TS25_2(t *testing.T) {
	s, err := gatewayd.New(gatewayd.Config{Backend: local.Unisolated(), NetworkModes: unrestricted,
		Tokens: []string{gwtest.Token, gwtest.TokenB}, Authenticate: acceptAll})
	if s != nil {
		t.Error("New returned a server for a Config with both Tokens and Authenticate")
	}
	if err == nil {
		t.Fatal("New returned no error for a Config with both Tokens and Authenticate")
	}
	for _, field := range []string{"Tokens", "Authenticate"} {
		if !strings.Contains(err.Error(), field) {
			t.Errorf("the error %q does not name %s", err, field)
		}
	}
	for _, tok := range []string{gwtest.Token, gwtest.TokenB} {
		if strings.Contains(err.Error(), tok) {
			t.Errorf("the error %q contains a configured token", err)
		}
	}
}

// TS-25-3: New refuses a Config with neither Tokens nor Authenticate, for
// nil and empty Tokens. 25-REQ-1.3.
func TestNewRefusesNeitherMode_TS25_3(t *testing.T) {
	for name, toks := range map[string][]string{"nil": nil, "empty": {}} {
		s, err := gatewayd.New(gatewayd.Config{Backend: local.Unisolated(), NetworkModes: unrestricted, Tokens: toks})
		if s != nil {
			t.Errorf("%s Tokens: New returned a server", name)
		}
		if err == nil {
			t.Errorf("%s Tokens: New returned no error", name)
			continue
		}
		for _, field := range []string{"Tokens", "Authenticate"} {
			if !strings.Contains(err.Error(), field) {
				t.Errorf("%s Tokens: the error %q does not name %s", name, err, field)
			}
		}
	}
}

// TS-25-4: static mode validates the token list with the unchanged spec 19
// errors. 25-REQ-1.4.
func TestStaticModeValidatesTokens_TS25_4(t *testing.T) {
	be := local.Unisolated()
	for name, list := range map[string][]string{
		"too short":     {tok40[:31]},
		"non-printable": {tok40[:20] + "\x01" + tok40[:20]},
		"repeated":      {tok40, tok40},
	} {
		want := token.Validate(list)
		if want == nil {
			t.Fatalf("%s: token.Validate accepts the list", name)
		}
		s, err := gatewayd.New(gatewayd.Config{Backend: be, NetworkModes: unrestricted, Tokens: list})
		if s != nil || err == nil {
			t.Errorf("%s: New = %v, %v; want a refusal", name, s, err)
			continue
		}
		if got, wantText := err.Error(), "gatewayd: Config.Tokens: "+want.Error(); got != wantText {
			t.Errorf("%s: the error is %q, want %q", name, got, wantText)
		}
	}
	if s, err := gatewayd.New(gatewayd.Config{Backend: be, NetworkModes: unrestricted, Tokens: []string{tok40}}); s == nil || err != nil {
		t.Errorf("a valid list: New = %v, %v; want a server", s, err)
	}
}

// TS-25-5: callback mode skips token validation but keeps the other spec 19
// checks. 25-REQ-1.5.
func TestCallbackModeKeepsOtherChecks_TS25_5(t *testing.T) {
	be := local.Unisolated()
	if s, err := gatewayd.New(gatewayd.Config{Backend: be, NetworkModes: unrestricted, Authenticate: acceptAll}); s == nil || err != nil {
		t.Errorf("a valid callback Config: New = %v, %v; want a server", s, err)
	}
	if _, err := gatewayd.New(gatewayd.Config{Authenticate: acceptAll}); err == nil || err.Error() != "gatewayd: Config.Backend is nil" {
		t.Errorf("a nil Backend: %v, want \"gatewayd: Config.Backend is nil\"", err)
	}
	_, err := gatewayd.New(gatewayd.Config{Backend: be, NetworkModes: unrestricted, Authenticate: acceptAll, MaxSandboxes: -1})
	if err == nil || err.Error() != "gatewayd: Config.MaxSandboxes is negative" {
		t.Errorf("a negative MaxSandboxes: %v, want \"gatewayd: Config.MaxSandboxes is negative\"", err)
	}
}
