package gatewayd_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// logged is a gateway under test and its captured log.
type logged struct {
	env  *gwtest.Env
	logs *gwtest.Logs
}

// startLogged serves a gateway with cfg over a Counting backend and a captured log.
func startLogged(t *testing.T, cfg gatewayd.Config) logged {
	t.Helper()
	logs, logger := gwtest.NewLogs()
	cfg.Backend, cfg.NetworkModes, cfg.Logger = gwtest.NewCounting(), unrestricted, logger
	return logged{env: gwtest.Start(t, cfg), logs: logs}
}

// staticAndCallback serves a static-mode gateway (Tokens [gwtest.Token]) and a
// callback-mode one (authenticator a), each with a captured log.
func staticAndCallback(t *testing.T, a *authn) map[string]logged {
	t.Helper()
	return map[string]logged{
		"static":   startLogged(t, gatewayd.Config{Tokens: []string{gwtest.Token}}),
		"callback": startLogged(t, gatewayd.Config{Authenticate: a.Authenticate}),
	}
}

// listWith makes a List RPC with ctx and ignores the answer.
func (l logged) listWith(t *testing.T, ctx context.Context) {
	t.Helper()
	_, _ = l.env.RawGateway(t).List(ctx, &sandboxpb.ListRequest{})
}

// refusedReasons is the reason of every 'authentication refused' record.
func (l logged) refusedReasons() []any {
	var out []any
	for _, rec := range l.logs.Messages("authentication refused") {
		out = append(out, rec["reason"])
	}
	return out
}

// noTokenContexts are outgoing contexts whose authorization is missing,
// repeated, not Bearer or an empty Bearer.
func noTokenContexts() map[string]context.Context {
	base := context.Background()
	return map[string]context.Context{
		"no header":    base,
		"two values":   metadata.AppendToOutgoingContext(base, "authorization", "Bearer aaaa", "authorization", "Bearer bbbb"),
		"basic":        gwtest.Ctx(base, "", "authorization", "Basic xyz"),
		"lower prefix": gwtest.Ctx(base, "", "authorization", "bearer x"),
		"empty token":  gwtest.Ctx(base, "", "authorization", "Bearer "),
	}
}

// TS-25-21: callback mode logs the full principal as the owner of a created
// sandbox. 25-REQ-6.1.
func TestCallbackLogsFullPrincipal_TS25_21(t *testing.T) {
	const principal = "tenant-1-lease-0042"
	l := startLogged(t, gatewayd.Config{Authenticate: newAuthn("tok-a", principal).Authenticate})
	create(t, l.env.Dial(t, "tok-a"), unrestrictedSpec)
	created := l.logs.Messages("sandbox created")
	if len(created) != 1 || created[0]["owner"] != principal {
		t.Errorf("sandbox created lines %v, want one with owner %q", created, principal)
	}
}

// TS-25-22: every authentication refusal logs the unchanged Warn message
// with peer and a reason, in both modes. 25-REQ-6.2.
func TestRefusalLogsPeerAndReason_TS25_22(t *testing.T) {
	reasons := []any{"no token", "refused", "invalid principal", "panic"}
	for name, l := range staticAndCallback(t, newAuthn()) {
		l.listWith(t, gwtest.Ctx(context.Background(), strings.Repeat("x", 40)))
		recs := l.logs.Messages("authentication refused")
		if len(recs) != 1 {
			t.Errorf("%s: %d authentication refused records, want 1", name, len(recs))
			continue
		}
		if recs[0]["level"] != "WARN" || recs[0]["peer"] == nil || recs[0]["peer"] == "" {
			t.Errorf("%s: the record %v is not a WARN with a peer", name, recs[0])
		}
		found := false
		for _, r := range reasons {
			found = found || recs[0]["reason"] == r
		}
		if !found {
			t.Errorf("%s: reason %v is none of %v", name, recs[0]["reason"], reasons)
		}
	}
}

// TS-25-23: a missing, repeated, non-Bearer or empty header is logged with
// reason "no token". 25-REQ-6.3.
func TestNoTokenReason_TS25_23(t *testing.T) {
	for mode, l := range staticAndCallback(t, newAuthn()) {
		for shape, ctx := range noTokenContexts() {
			l.listWith(t, ctx)
			reasons := l.refusedReasons()
			if len(reasons) == 0 || reasons[len(reasons)-1] != "no token" {
				t.Errorf("%s, %s: the refusal reasons are %v, want the last to be \"no token\"", mode, shape, reasons)
			}
		}
		if n := len(l.refusedReasons()); n != len(noTokenContexts()) {
			t.Errorf("%s: %d refusals logged for %d refused RPCs", mode, n, len(noTokenContexts()))
		}
	}
}

// TS-25-24: a hash mismatch or an authenticator answering false is logged
// with reason "refused". 25-REQ-6.4.
func TestRefusedReason_TS25_24(t *testing.T) {
	for mode, l := range staticAndCallback(t, newAuthn()) {
		l.listWith(t, gwtest.Ctx(context.Background(), strings.Repeat("x", 40)))
		if got := l.refusedReasons(); len(got) != 1 || got[0] != "refused" {
			t.Errorf("%s: the refusal reasons are %v, want [refused]", mode, got)
		}
	}
}

// TS-25-25: an empty or over-long principal is logged with reason
// "invalid principal". 25-REQ-6.5.
func TestInvalidPrincipalReason_TS25_25(t *testing.T) {
	l := startLogged(t, gatewayd.Config{Authenticate: newAuthn("tok-empty", "", "tok-long", strings.Repeat("a", 257)).Authenticate})
	for _, tok := range []string{"tok-empty", "tok-long"} {
		l.listWith(t, gwtest.Ctx(context.Background(), tok))
	}
	got := l.refusedReasons()
	if len(got) != 2 || got[0] != "invalid principal" || got[1] != "invalid principal" {
		t.Errorf("the refusal reasons are %v, want two of \"invalid principal\"", got)
	}
}

// TS-25-26: a recovered authenticator panic is logged with reason "panic" and
// without the panic value. 25-REQ-6.6.
func TestPanicReason_TS25_26(t *testing.T) {
	const secret = "PANIC-SECRET-xyz"
	l := startLogged(t, gatewayd.Config{Authenticate: func(context.Context, string) (string, bool) { panic(secret) }})
	l.listWith(t, gwtest.Ctx(context.Background(), "tok"))
	if got := l.refusedReasons(); len(got) != 1 || got[0] != "panic" {
		t.Errorf("the refusal reasons are %v, want [panic]", got)
	}
	if strings.Contains(l.logs.String(), secret) {
		t.Error("the log contains the panic value")
	}
}

// TS-25-27: no log line carries a token, a panic value, a full static hash,
// an Env value or a tool payload, in either mode. 25-REQ-6.7.
func TestNoSecretsLogged_TS25_27(t *testing.T) {
	const (
		plantedToken   = "PLANTED-TOKEN-0123456789abcdefghijklmnopqrstuvwxyz"
		plantedPanic   = "PLANTED-PANIC-VALUE-qwerty"
		plantedEnv     = "PLANTED-ENV-VALUE-asdfgh"
		plantedPayload = "PLANTED-TOOL-PAYLOAD-zxcvbn"
	)
	authenticate := func(_ context.Context, tok string) (string, bool) {
		switch tok {
		case plantedToken:
			return "tenant-1", true
		case "tok-boom":
			panic(plantedPanic)
		}
		return "", false
	}
	servers := map[string]logged{
		"static":   startLogged(t, gatewayd.Config{Tokens: []string{plantedToken}}),
		"callback": startLogged(t, gatewayd.Config{Authenticate: authenticate}),
	}
	for mode, l := range servers {
		p := l.env.Dial(t, plantedToken)
		ctx := context.Background()
		spec := sandbox.Spec{Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted}, TTL: time.Hour, Env: map[string]string{"API_KEY": plantedEnv}}
		sb1, err := p.Create(ctx, spec)
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		create(t, p, unrestrictedSpec) // left to Shutdown, below
		in, _ := json.Marshal(map[string]any{"path": "f.txt", "content": plantedPayload})
		if r := toolOf(t, sb1, "write_file").Execute(ctx, in); !r.OK {
			t.Fatalf("%s: write_file: %+v", mode, r)
		}
		l.listWith(t, gwtest.Ctx(ctx, "tok-boom"))
		l.listWith(t, gwtest.Ctx(ctx, strings.Repeat("x", 40)))
		l.listWith(t, ctx)
		if err := sb1.Close(ctx); err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err := l.env.Server.Shutdown(sctx); err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		cancel()

		out := l.logs.String()
		secrets := []string{plantedToken, plantedPanic, plantedEnv, plantedPayload, hexSHA256(plantedToken)}
		for _, secret := range secrets {
			if strings.Contains(out, secret) {
				t.Errorf("%s: the log contains %q", mode, secret)
			}
		}
		if len(l.logs.Messages("sandbox created")) != 2 || len(l.logs.Messages("authentication refused")) != 3 {
			t.Errorf("%s: the flows did not log what they should have:\n%s", mode, out)
		}
	}
}
