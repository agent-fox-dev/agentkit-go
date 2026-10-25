package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/buildver"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
	"github.com/agent-fox-dev/agentkit-go/sandbox/podman"
)

func TestVersionPrintsTheBuildAndBothProtocolVersions(t *testing.T) {
	var out bytes.Buffer
	want := fmt.Sprintf("agentkit-gateway %s (gateway protocol %d.%d, agent protocol %d.%d)\n", buildver.Version(),
		gateway.ProtocolMajor, gateway.ProtocolMinor, sandbox.ProtocolMajor, sandbox.ProtocolMinor)
	if code := run([]string{"--version"}, &out, io.Discard, deps{}); code != 0 || out.String() != want {
		t.Errorf("--version: exit %d, %q; want 0, %q", code, out.String(), want)
	}
}

// A startup failure exits 1 with one stderr line that never holds a token.
func TestAStartupFailureExitsOneWithOneLine(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	defer busy.Close()
	tokens, short, notPEM := file(t, tok40+"\n"), file(t, "short-token-20-chars\n"), file(t, "not a certificate")
	stub := backend(gwtest.NewCounting(), new(podman.Options)) // over local, which offers only the unrestricted network
	for _, c := range []struct {
		args       []string
		newBackend func(podman.Options) (sandbox.Provider, error)
	}{
		{[]string{"--token-file", "/nonexistent/tokens", "--plaintext"}, stub},
		{[]string{"--token-file", short, "--plaintext"}, stub},
		{[]string{"--token-file", tokens, "--tls-cert", notPEM, "--tls-key", notPEM}, stub},
		{[]string{"--token-file", tokens, "--plaintext", "--engine-binary", "/nonexistent/podman"}, podman.New},
		{[]string{"--token-file", tokens, "--plaintext", "--network", "none"}, stub},
		{[]string{"--token-file", tokens, "--plaintext", "--network", "unrestricted", "--listen", busy.Addr().String()}, stub},
	} {
		var stderr bytes.Buffer
		code := run(c.args, io.Discard, &stderr, deps{newBackend: c.newBackend})
		lines := strings.Split(strings.TrimRight(stderr.String(), "\n"), "\n")
		if code != 1 || len(lines) != 1 || !strings.HasPrefix(lines[0], "agentkit-gateway: ") || strings.Contains(lines[0], tok40) ||
			strings.Contains(lines[0], "short-token") {
			t.Errorf("%v: exit %d, stderr %q; want 1 and one agentkit-gateway: line without a token", c.args, code, stderr.String())
		}
	}
}

// The command serves in static mode with the token file's tokens and the
// flags' engine options and limits, logs each refusal with its reason, and
// warns only when plaintext leaves loopback.
func TestTheCommandServesTheFlagsInStaticMode(t *testing.T) {
	var got podman.Options
	s := serve(t, backend(gwtest.NewCounting(), &got), "--plaintext", "--max-sandboxes", "3", "--max-ttl", "2h",
		"--engine", "docker", "--engine-binary", "eng", "--runtime", "krun", "--userns", "keep-id")
	p, err := gateway.Dial(context.Background(), "http://"+s.addr, gateway.Options{Token: tok40})
	must(t, err)
	defer p.Close()
	if g := p.Gateway(); g.MaxSandboxes != 3 || g.MaxTTL != 2*time.Hour || got.Engine != "docker" || got.Binary != "eng" ||
		got.Runtime != "krun" || got.UserNS != "keep-id" || len(got.EngineEnv) == 0 {
		t.Errorf("the gateway serves %+v over a backend made with %+v", g, got)
	}
	cc, err := grpc.NewClient(s.addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	must(t, err)
	defer cc.Close()
	for _, ctx := range []context.Context{context.Background(), gwtest.Ctx(context.Background(), gwtest.Token)} {
		_, _ = sandboxpb.NewGatewayClient(cc).List(ctx, &sandboxpb.ListRequest{})
	}
	if code := s.stop(t, syscall.SIGTERM); code != 0 {
		t.Errorf("exit %d after SIGTERM, want 0", code)
	}
	refused := regexp.MustCompile(`(?m)msg="authentication refused" peer=\S+ reason="?([^"\n]+)"?$`).FindAllStringSubmatch(s.stderr.String(), -1)
	if len(refused) != 2 || refused[0][1] != "no token" || refused[1][1] != "refused" || strings.Contains(s.stderr.String(), "WARN msg=\"serving plaintext") {
		t.Errorf("want two refusals, no token then refused, and no plaintext warning:\n%s", s.stderr)
	}
	off := serve(t, backend(gwtest.NewCounting(), &got), "--plaintext", "--listen", "0.0.0.0:0")
	off.stop(t, syscall.SIGTERM)
	if !regexp.MustCompile(`level=WARN msg="serving plaintext on a non-loopback address.*address=0\.0\.0\.0:0`).MatchString(off.stderr.String()) {
		t.Errorf("no plaintext warning naming 0.0.0.0:\n%s", off.stderr)
	}
}

// A stop signal shuts the gateway down, closing its sandboxes, and exits 0,
// or 1 with one line naming the failure when a Close failed.
func TestAStopSignalShutsTheGatewayDown(t *testing.T) {
	for _, c := range []struct {
		sig      os.Signal
		closeErr error
		want     int
	}{{syscall.SIGTERM, nil, 0}, {syscall.SIGINT, nil, 0}, {syscall.SIGINT, errors.New("close failed"), 1}} {
		be := gwtest.NewCounting()
		be.SetCloseErr(c.closeErr)
		s := serve(t, backend(be, new(podman.Options)), "--plaintext")
		p, err := gateway.Dial(context.Background(), "http://"+s.addr, gateway.Options{Token: tok40})
		must(t, err)
		if _, err := p.Create(context.Background(), sandbox.Spec{Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted}}); err != nil {
			t.Fatal(err)
		}
		code := s.stop(t, c.sig)
		_ = p.Close()
		failures := regexp.MustCompile(`(?m)^agentkit-gateway: .*close failed`).FindAllString(s.stderr.String(), -1)
		if code != c.want || be.Closes() != 1 || len(failures) != c.want {
			t.Errorf("%v, Close error %v: exit %d after %d closes, failure lines %q; want %d", c.sig, c.closeErr, code, be.Closes(), failures, c.want)
		}
	}
}
