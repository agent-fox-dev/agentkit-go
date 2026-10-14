package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/buildver"
	"github.com/agent-fox-dev/agentkit-go/sandbox/podman"
)

// deps are what run takes from its environment, so tests can replace them.
type deps struct {
	newBackend func(podman.Options) (sandbox.Provider, error)
	signals    <-chan os.Signal
}

// shutdownBudget bounds Shutdown after a signal.
const shutdownBudget = 60 * time.Second

// run is the command; it returns the exit code.
func run(args []string, stdout, stderr io.Writer, d deps) int {
	fail := func(code int, format string, a ...any) int {
		fmt.Fprintf(stderr, "agentkit-gateway: "+format+"\n", a...)
		return code
	}
	o, err := parseFlags(args)
	if err != nil {
		return fail(2, "%v", err)
	}
	if o.version {
		fmt.Fprintf(stdout, "agentkit-gateway %s (gateway protocol %d.%d, agent protocol %d.%d)\n", buildver.Version(),
			gateway.ProtocolMajor, gateway.ProtocolMinor, sandbox.ProtocolMajor, sandbox.ProtocolMinor)
		return 0
	}
	tokens, err := readTokenFile(o.tokenFile)
	if err != nil {
		return fail(1, "%v", err)
	}
	var tlsCfg *tls.Config
	if o.tlsCert != "" {
		cert, err := tls.LoadX509KeyPair(o.tlsCert, o.tlsKey)
		if err != nil {
			return fail(1, "TLS: %v", err)
		}
		tlsCfg = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	}
	backend, err := d.newBackend(podman.Options{Binary: o.engineBinary, Engine: podman.Engine(o.engine), Runtime: o.runtime,
		UserNS: o.userns, EngineEnv: os.Environ()})
	if err != nil {
		return fail(1, "backend: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(stderr, nil))
	srv, err := gatewayd.New(gatewayd.Config{Backend: backend, Tokens: tokens, TLS: tlsCfg, NetworkModes: o.network,
		MaxSandboxes: o.maxSandboxes, MaxTTL: o.maxTTL, ReapInterval: o.reapInterval, Logger: logger})
	if err != nil {
		return fail(1, "%v", err)
	}
	l, err := net.Listen("tcp", o.listen)
	if err != nil {
		return fail(1, "listen: %v", err)
	}
	mode := "tls"
	if o.plaintext {
		mode = "plaintext"
	}
	fmt.Fprintf(stderr, "agentkit-gateway: listening on %s (%s), backend %s, version %s\n", l.Addr(), mode,
		backend.Capabilities().Provider, buildver.Version())
	if o.plaintext && !loopback(o.listen) {
		logger.Warn("serving plaintext on a non-loopback address: bearer tokens cross the network in clear text",
			"address", o.listen, "bound", l.Addr().String())
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(l) }()
	var serveErr error
	select {
	case serveErr = <-served:
	case <-d.signals:
	}
	ctx, cancel := context.WithTimeout(context.Background(), shutdownBudget)
	defer cancel()
	shutdownErr := srv.Shutdown(ctx)
	switch {
	case serveErr != nil:
		return fail(1, "serve: %v", errors.Join(serveErr, shutdownErr))
	case shutdownErr != nil:
		return fail(1, "shutdown: %v", shutdownErr)
	}
	<-served
	return 0
}

// loopback reports whether the listen address hostport names localhost or a
// loopback IP; an empty host listens on every interface.
func loopback(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	a, err := netip.ParseAddr(host)
	return err == nil && a.IsLoopback()
}
