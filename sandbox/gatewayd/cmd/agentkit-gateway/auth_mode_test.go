package main

import (
	"bytes"
	"context"
	"os"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
	"github.com/agent-fox-dev/agentkit-go/sandbox/podman"
)

// refusedLine matches a refused-authentication log line of the command's text
// handler and captures its peer and reason.
var refusedLine = regexp.MustCompile(`(?m)msg="authentication refused" peer=(\S+) reason="?([^"\n]+)"?$`)

// TS-25-39: the command stays in static mode and logs a refusal with reason
// "no token" or "refused". 25-REQ-9.1.
func TestCommandStaysStatic_TS25_39(t *testing.T) {
	tokens := writeFile(t, t.TempDir(), "tokens", tok40+"\n", 0o600)
	be := gwtest.NewCounting()
	sigs := make(chan os.Signal, 1)
	var stderr syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- run([]string{"--token-file", tokens, "--plaintext", "--listen", "127.0.0.1:0", "--network", "unrestricted"},
			&bytes.Buffer{}, &stderr, deps{newBackend: func(podman.Options) (sandbox.Provider, error) { return be, nil }, signals: sigs})
	}()
	addr := waitListening(t, &stderr)
	cc, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	gw := sandboxpb.NewGatewayClient(cc)
	ctx := context.Background()
	// In this order: the log lines below are asserted in it.
	for _, c := range []struct {
		name string
		ctx  context.Context
	}{{"no token", ctx}, {"wrong token", gwtest.Ctx(ctx, strings.Repeat("x", 40))}} {
		if _, err := gw.List(c.ctx, &sandboxpb.ListRequest{}); status.Code(err) != codes.Unauthenticated {
			t.Errorf("%s: %v, want Unauthenticated", c.name, err)
		}
	}
	if _, err := gw.List(gwtest.Ctx(ctx, tok40), &sandboxpb.ListRequest{}); err != nil {
		t.Errorf("the token from the token file: %v", err)
	}

	sigs <- syscall.SIGTERM
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit %d after SIGTERM, want 0; stderr:\n%s", code, stderr.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatal("run did not return after the signal")
	}

	lines := refusedLine.FindAllStringSubmatch(stderr.String(), -1)
	if len(lines) != 2 || lines[0][2] != "no token" || lines[1][2] != "refused" || lines[0][1] == "" || lines[1][1] == "" {
		t.Errorf("the refused-authentication lines are %q, want a peer and the reasons \"no token\" then \"refused\":\n%s", lines, stderr.String())
	}

	src, err := os.ReadFile("run.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "Tokens: tokens") || strings.Contains(string(src), "Authenticate") {
		t.Error("run.go must fill Config.Tokens from the token file and never mention Authenticate")
	}
}
