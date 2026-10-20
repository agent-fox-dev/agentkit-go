package gatewayd_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/doc"
	"go/parser"
	"go/token"
	"go/types"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// refusalText is every refused RPC's message.
const refusalText = "gatewayd: a valid bearer token is required"

// hexSHA256 is the 64-character lowercase hex SHA-256 of s.
func hexSHA256(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// TS-25-6: static mode refuses an unknown token in the tap and the
// interceptors with the spec 19 text. 25-REQ-2.1.
func TestStaticRefusesUnknownToken_TS25_6(t *testing.T) {
	env := gwtest.Start(t, gatewayd.Config{Backend: gwtest.NewAgents(nil), NetworkModes: unrestricted, Tokens: []string{gwtest.Token}})
	cc := env.Raw(t)
	gw, ag := sandboxpb.NewGatewayClient(cc), sandboxpb.NewAgentClient(cc)
	ctx := context.Background()
	unknown := strings.Repeat("x", 40)

	refused := map[string]error{}
	_, refused["unary"] = gw.List(gwtest.Ctx(ctx, unknown), &sandboxpb.ListRequest{})
	refused["stream"] = firstRecv(ag.SyncTar(gwtest.Ctx(ctx, unknown)))
	refused["unserved method"] = cc.Invoke(gwtest.Ctx(ctx, unknown), "/agentkit.sandbox.v1.Agent/Future", &sandboxpb.HelloRequest{}, &sandboxpb.HelloResponse{})
	for name, err := range refused {
		if status.Code(err) != codes.Unauthenticated || status.Convert(err).Message() != refusalText {
			t.Errorf("%s with an unknown token: %v, want Unauthenticated %q", name, err, refusalText)
		}
	}

	if _, err := gw.List(gwtest.Ctx(ctx, gwtest.Token), &sandboxpb.ListRequest{}); err != nil {
		t.Errorf("unary with a configured token: %v", err)
	}
	id := rawCreate(t, gw, gwtest.Token, sandboxpb.NetworkMode_UNRESTRICTED)
	st, err := ag.SyncTar(gwtest.Ctx(ctx, gwtest.Token, gateway.SandboxIDKey, id))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Send(&sandboxpb.TarChunk{Body: &sandboxpb.TarChunk_Data{Data: []byte{1}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CloseAndRecv(); err != nil {
		t.Errorf("stream with a configured token: %v", err)
	}
}

// TS-25-7 (property): in static mode the principal is the lowercase hex
// SHA-256 of any accepted token. 25-REQ-2.2.
func TestStaticPrincipalIsHexSHA256_TS25_7(t *testing.T) {
	r := rand.New(rand.NewPCG(25, 7))
	for i := range 20 {
		tok := make([]byte, 32+r.IntN(33))
		for j := range tok {
			tok[j] = byte(0x21 + r.IntN(0x7e-0x21+1))
		}
		t.Run(fmt.Sprintf("token-%d", i), func(t *testing.T) {
			be := gwtest.NewCounting()
			env := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: unrestricted, Tokens: []string{string(tok)}})
			create(t, env.Dial(t, string(tok)), unrestrictedSpec)
			got := be.Principals()
			if len(got) != 1 {
				t.Fatalf("the backend recorded %d Create principals, want 1", len(got))
			}
			want := hexSHA256(string(tok))
			if !got[0].OK || got[0].Principal != want || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(got[0].Principal) {
				t.Errorf("Create principal = (%q, %v), want (%q, true)", got[0].Principal, got[0].OK, want)
			}
		})
	}
}

// TS-25-8: static mode logs the 8-character owner fingerprint and never the
// full hash. 25-REQ-2.3.
func TestStaticLogsFingerprint_TS25_8(t *testing.T) {
	logs, logger := gwtest.NewLogs()
	env := gwtest.Start(t, gatewayd.Config{Backend: gwtest.NewCounting(), NetworkModes: unrestricted, Logger: logger, Tokens: []string{gwtest.Token}})
	create(t, env.Dial(t, gwtest.Token), unrestrictedSpec)
	full := hexSHA256(gwtest.Token)
	created := logs.Messages("sandbox created")
	if len(created) != 1 || created[0]["owner"] != full[:8] {
		t.Errorf("sandbox created lines %v, want one with owner %s", created, full[:8])
	}
	out := logs.String()
	if strings.Contains(out, full) || strings.Contains(out, gwtest.Token) {
		t.Error("the log contains the full hash or the token")
	}
}

// TS-25-28: PrincipalFromContext is exported and nothing exported puts a
// principal on a context. 25-REQ-7.1.
func TestNoExportedWayToSetAPrincipal_TS25_28(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, e := range entries {
		if n := e.Name(); strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			f, err := parser.ParseFile(fset, filepath.Join(".", n), nil, parser.ParseComments)
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, f)
		}
	}
	pkg, err := doc.NewFromFiles(fset, files, "github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd")
	if err != nil {
		t.Fatal(err)
	}
	sig := func(fn *doc.Func) string { return types.ExprString(fn.Decl.Type) }
	funcs := slices.Clone(pkg.Funcs)
	for _, typ := range pkg.Types {
		funcs = append(funcs, typ.Funcs...)
		funcs = append(funcs, typ.Methods...)
	}
	var found bool
	for _, fn := range funcs {
		switch {
		case fn.Name == "PrincipalFromContext":
			found = true
			if got, want := sig(fn), "func(ctx context.Context) (principal string, ok bool)"; got != want {
				t.Errorf("PrincipalFromContext has the signature %s, want %s", got, want)
			}
		case strings.Contains(sig(fn), ") context.Context") || strings.Contains(sig(fn), ", context.Context"):
			t.Errorf("exported %s returns a context: %s", fn.Name, sig(fn))
		}
	}
	if !found {
		t.Error("PrincipalFromContext is not exported")
	}
	var names []string
	for _, v := range append(slices.Clone(pkg.Vars), pkg.Consts...) {
		names = append(names, v.Names...)
	}
	for _, typ := range pkg.Types {
		names = append(names, typ.Name)
	}
	for _, n := range names {
		if strings.Contains(n, "Key") || strings.Contains(n, "Context") {
			t.Errorf("exported %s looks like a context key", n)
		}
	}
}

// TS-25-32: PrincipalFromContext reports no principal on contexts the server
// did not make. 25-REQ-7.7.
func TestPrincipalFromContextForeign_TS25_32(t *testing.T) {
	type otherKey struct{}
	for name, ctx := range map[string]context.Context{
		"background":  context.Background(),
		"string key":  context.WithValue(context.Background(), "principal", "x"), //nolint:staticcheck // the point is a string key
		"foreign key": context.WithValue(context.Background(), otherKey{}, "x"),
		"cancelled":   func() context.Context { c, cancel := context.WithCancel(context.Background()); cancel(); return c }(),
	} {
		if p, ok := gatewayd.PrincipalFromContext(ctx); p != "" || ok {
			t.Errorf("%s: PrincipalFromContext = (%q, %v), want (\"\", false)", name, p, ok)
		}
	}
}

// TS-25-40: static mode keeps spec 19's refusals, status codes, texts and log
// messages, adding only reason. 25-REQ-9.2.
func TestStaticKeepsSpec19Refusal_TS25_40(t *testing.T) {
	logs, logger := gwtest.NewLogs()
	env := gwtest.Start(t, gatewayd.Config{Backend: gwtest.NewCounting(), NetworkModes: unrestricted, Logger: logger, Tokens: []string{gwtest.Token}})
	gw := env.RawGateway(t)
	_, err := gw.List(gwtest.Ctx(context.Background(), strings.Repeat("x", 40)), &sandboxpb.ListRequest{})
	if status.Code(err) != codes.Unauthenticated || status.Convert(err).Message() != refusalText {
		t.Errorf("a refused RPC: %v, want Unauthenticated %q", err, refusalText)
	}
	recs := logs.Messages("authentication refused")
	if len(recs) != 1 {
		t.Fatalf("%d authentication refused lines, want 1: %s", len(recs), logs.String())
	}
	if recs[0]["level"] != "WARN" {
		t.Errorf("level %v, want WARN", recs[0]["level"])
	}
	var keys []string
	for k := range recs[0] {
		if k != "time" && k != "level" && k != "msg" {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"peer", "reason"}) {
		t.Errorf("the record's attributes are %v, want [peer reason]", keys)
	}
}
