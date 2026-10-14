package gateway

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"time"
)

// Options configures Dial.
type Options struct {
	// Token is the gateway's bearer token: non-empty printable ASCII without
	// whitespace. It is sent on every RPC, and for https only over TLS.
	Token string
	// TLSConfig is cloned for an https URL; nil means the system roots with
	// a minimum of TLS 1.2. An empty ServerName becomes the URL's host.
	TLSConfig *tls.Config
	// Dialer, when set, replaces TCP; it is given host:port. The URL rules
	// still apply.
	Dialer func(context.Context, string) (net.Conn, error)
}

// Info is one sandbox the caller holds on the gateway.
type Info struct {
	ID      string
	Labels  map[string]string
	Created time.Time
	Expires time.Time
}

// GatewayInfo is what the gateway reported at Dial.
type GatewayInfo struct {
	// URL is the URL Dial was given.
	URL string
	// Version is the gateway's build version.
	Version string
	// Backend is the backend provider's name.
	Backend string
	// ProtocolMajor and ProtocolMinor are the gateway's protocol version.
	ProtocolMajor uint32
	ProtocolMinor uint32
	// MaxTTL is the longest TTL the gateway accepts.
	MaxTTL time.Duration
	// MaxSandboxes is the most sandboxes the gateway holds at once.
	MaxSandboxes int
}

var (
	// ErrUnauthenticated: the gateway refused the token.
	ErrUnauthenticated = errors.New("gateway: unauthenticated")
	// ErrCapacity: the gateway holds as many sandboxes as it allows.
	ErrCapacity = errors.New("gateway: at capacity")
	// ErrClosed: the Provider has been closed.
	ErrClosed = errors.New("gateway: provider closed")
)
