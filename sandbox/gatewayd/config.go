package gatewayd

import (
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/token"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// Config configures a Server.
type Config struct {
	// Backend creates the sandboxes. Required.
	Backend sandbox.Provider
	// Tokens are the accepted bearer tokens: at least one, each at least 32
	// characters of printable ASCII without whitespace, none repeated.
	Tokens []string
	// TLS, when set, serves TLS with it; nil serves plaintext (behind a TLS
	// proxy, or on loopback).
	TLS *tls.Config
	// NetworkModes is the network ceiling: the modes clients may request,
	// intersected with the backend's. Nil means [NetworkNone].
	NetworkModes []sandbox.NetworkMode
	// MaxSandboxes is the most sandboxes held at once; zero means 16.
	MaxSandboxes int
	// MaxTTL is the longest TTL accepted; zero means 24h.
	MaxTTL time.Duration
	// ReapInterval is how often Backend.Reap runs; zero means 10m.
	ReapInterval time.Duration
	// Logger receives one structured line per create, destroy, refused
	// authentication and backend reap; nil discards.
	Logger *slog.Logger
}

// The defaults New applies to zero optional fields.
const (
	defaultMaxSandboxes = 16
	defaultMaxTTL       = 24 * time.Hour
	defaultReapInterval = 10 * time.Minute
	// defaultTTL is what a zero Spec.TTL means.
	defaultTTL = time.Hour
)

// Server is a gateway. Serve it once; Shutdown stops it.
type Server struct {
	backend sandbox.Provider
	caps    sandbox.Capabilities
	// offered is the network ceiling intersected with the backend's modes.
	offered      []sandbox.NetworkMode
	maxSandboxes int
	maxTTL       time.Duration
	reapInterval time.Duration
	log          *slog.Logger
	// owners are the SHA-256 sums of the accepted tokens.
	owners [][sha256.Size]byte

	srv *grpc.Server
	reg *registry

	serveOnce    sync.Once
	shutdownOnce sync.Once
	shutdownErr  error
	stopped      chan struct{}
}

// New checks cfg, applies its defaults and returns a Server that is not yet
// serving.
func New(cfg Config) (*Server, error) {
	if cfg.Backend == nil {
		return nil, errors.New("gatewayd: Config.Backend is nil")
	}
	if err := token.Validate(cfg.Tokens); err != nil {
		return nil, fmt.Errorf("gatewayd: Config.Tokens: %w", err)
	}
	for _, f := range []struct {
		name string
		neg  bool
	}{{"MaxSandboxes", cfg.MaxSandboxes < 0}, {"MaxTTL", cfg.MaxTTL < 0}, {"ReapInterval", cfg.ReapInterval < 0}} {
		if f.neg {
			return nil, fmt.Errorf("gatewayd: Config.%s is negative", f.name)
		}
	}
	caps := cfg.Backend.Capabilities()
	ceiling := cfg.NetworkModes
	if ceiling == nil {
		ceiling = []sandbox.NetworkMode{sandbox.NetworkNone}
	}
	var offered []sandbox.NetworkMode
	for _, m := range caps.Network {
		if slices.Contains(ceiling, m) {
			offered = append(offered, m)
		}
	}
	if len(offered) == 0 {
		return nil, fmt.Errorf("gatewayd: Config.NetworkModes %s and the backend %q's network modes %s have no mode in common",
			modeList(ceiling), caps.Provider, modeList(caps.Network))
	}
	s := &Server{backend: cfg.Backend, caps: caps, offered: offered, maxSandboxes: cfg.MaxSandboxes, maxTTL: cfg.MaxTTL,
		reapInterval: cfg.ReapInterval, log: cfg.Logger, stopped: make(chan struct{})}
	if s.maxSandboxes == 0 {
		s.maxSandboxes = defaultMaxSandboxes
	}
	if s.maxTTL == 0 {
		s.maxTTL = defaultMaxTTL
	}
	if s.reapInterval == 0 {
		s.reapInterval = defaultReapInterval
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	for _, t := range cfg.Tokens {
		s.owners = append(s.owners, sha256.Sum256([]byte(t)))
	}
	s.reg = newRegistry(s)
	opts := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(sandbox.MaxMessageBytes), grpc.MaxSendMsgSize(sandbox.MaxMessageBytes),
		grpc.ChainUnaryInterceptor(s.unaryAuth), grpc.ChainStreamInterceptor(s.streamAuth),
	}
	if cfg.TLS != nil {
		opts = append(opts, grpc.Creds(credentials.NewTLS(cfg.TLS.Clone())))
	}
	s.srv = grpc.NewServer(opts...)
	sandboxpb.RegisterGatewayServer(s.srv, &gatewayService{s: s})
	sandboxpb.RegisterAgentServer(s.srv, &relay{s: s})
	return s, nil
}

// modeList is modes as "[none, unrestricted]".
func modeList(modes []sandbox.NetworkMode) string {
	names := make([]string, len(modes))
	for i, m := range modes {
		names[i] = m.String()
	}
	return "[" + strings.Join(names, ", ") + "]"
}
