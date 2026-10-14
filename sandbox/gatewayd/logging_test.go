package gatewayd_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/localtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// TS-19-35: the server logs creates, destroys, refused authentications and
// reaps with an 8-hex owner fingerprint and never a token, an Env value or
// a tool payload. 19-REQ-5.5, 19-REQ-5.7.
func TestLogging_TS19_35(t *testing.T) {
	be := gwtest.NewCounting()
	logs, logger := gwtest.NewLogs()
	env := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: unrestricted, Logger: logger})
	p := env.Dial(t, gwtest.Token)
	ctx := context.Background()
	spec := func(ttl time.Duration, env map[string]string) sandbox.Spec {
		return sandbox.Spec{Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted}, TTL: ttl, Env: env}
	}
	s1, err := p.Create(ctx, spec(time.Hour, map[string]string{"API_KEY": "env-secret-value"}))
	if err != nil {
		t.Fatal(err)
	}
	s2, err := p.Create(ctx, spec(time.Second, nil))
	if err != nil {
		t.Fatal(err)
	}
	s3, err := p.Create(ctx, spec(time.Hour, nil))
	if err != nil {
		t.Fatal(err)
	}
	s4, err := p.Create(ctx, spec(time.Hour, nil))
	if err != nil {
		t.Fatal(err)
	}
	in, _ := json.Marshal(map[string]any{"path": "f.txt", "content": "tool-payload-secret"})
	if r := toolOf(t, s1, "write_file").Execute(ctx, in); !r.OK {
		t.Fatalf("write_file: %+v", r)
	}
	if err := s1.Close(ctx); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the 1s sandbox to expire", func() bool { return destroyReason(logs, s2.ID()) != "" })
	if err := localtest.Cut(be.ByID(s3.ID()).Sandbox); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the cut sandbox to be removed", func() bool {
		_, _ = p.List(ctx)
		return destroyReason(logs, s3.ID()) != ""
	})
	_, _ = env.RawGateway(t).List(ctx, &sandboxpb.ListRequest{})
	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := env.Server.Shutdown(sctx); err != nil {
		t.Fatal(err)
	}

	sum := sha256.Sum256([]byte(gwtest.Token))
	full := hex.EncodeToString(sum[:])
	for _, m := range logs.Messages("sandbox created") {
		if m["owner"] != full[:8] || m["id"] == "" || m["ttl"] == nil || m["network"] == nil {
			t.Errorf("create line %v lacks id, owner %s, ttl or network", m, full[:8])
		}
	}
	for id, want := range map[string]string{s1.ID(): "client", s2.ID(): "expired", s3.ID(): "unavailable", s4.ID(): "shutdown"} {
		if got := destroyReason(logs, id); got != want {
			t.Errorf("sandbox %s destroyed with reason %q, want %q", id, got, want)
		}
	}
	refused := logs.Messages("authentication refused")
	if len(refused) == 0 || refused[0]["peer"] == nil || refused[0]["peer"] == "" {
		t.Error("no refused-authentication line with a peer address")
	}
	if len(logs.Messages("backend reap")) == 0 {
		t.Error("no backend reap line")
	}
	out := logs.String()
	for _, secret := range []string{gwtest.Token, full, "env-secret-value", "tool-payload-secret"} {
		if strings.Contains(out, secret) {
			t.Errorf("the log contains %q", secret)
		}
	}
}
