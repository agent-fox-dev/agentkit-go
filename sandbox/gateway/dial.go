package gateway

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/gatewaywire"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// Dial connects to the gateway at rawURL and returns a Provider for it. The
// URL is https://host[:port] (TLS, port 443 by default), or
// http://host[:port] (plaintext, port 80 by default) only when the host is
// localhost or a loopback IP literal; it carries no user information,
// query, fragment or path other than "/". The URL and the token are checked
// before any connection is attempted.
//
// Dial calls the gateway's Capabilities under ctx and caches the answer. A
// refused token returns an error matching ErrUnauthenticated, a gateway of
// another protocol major one wrapping sandbox.ErrProtocol, and any other
// failure an error naming the URL.
func Dial(ctx context.Context, rawURL string, o Options) (*Provider, error) {
	target, host, useTLS, err := parseURL(rawURL)
	if err != nil {
		return nil, err
	}
	if err := checkToken(o.Token); err != nil {
		return nil, err
	}
	dialer := o.Dialer
	if dialer == nil {
		var d net.Dialer
		dialer = func(ctx context.Context, addr string) (net.Conn, error) { return d.DialContext(ctx, "tcp", addr) }
	}
	creds := insecure.NewCredentials()
	if useTLS {
		cfg := o.TLSConfig.Clone()
		if cfg == nil {
			cfg = &tls.Config{MinVersion: tls.VersionTLS12}
		}
		if cfg.ServerName == "" {
			cfg.ServerName = host
		}
		creds = credentials.NewTLS(cfg)
	}
	cc, err := grpc.NewClient("passthrough:///"+target,
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(creds),
		grpc.WithPerRPCCredentials(bearer{token: o.Token, requireTLS: useTLS}),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(sandbox.MaxMessageBytes), grpc.MaxCallSendMsgSize(sandbox.MaxMessageBytes)))
	if err != nil {
		return nil, fmt.Errorf("gateway: %s: %w", rawURL, err)
	}
	p := &Provider{url: rawURL, cc: cc, gw: sandboxpb.NewGatewayClient(cc)}
	resp, err := p.gw.Capabilities(ctx, &sandboxpb.CapabilitiesRequest{})
	if err == nil && resp.GetProtocolMajor() != ProtocolMajor {
		err = fmt.Errorf("%w: gateway %s speaks gateway protocol %d.%d, this client %d.%d", sandbox.ErrProtocol,
			rawURL, resp.GetProtocolMajor(), resp.GetProtocolMinor(), ProtocolMajor, ProtocolMinor)
	} else if err != nil {
		err = p.rpcErr("capabilities", err)
	}
	if err != nil {
		return nil, errors.Join(err, cc.Close())
	}
	maxTTL := time.Duration(resp.GetMaxTtlNanos())
	p.info = GatewayInfo{URL: rawURL, Version: resp.GetVersion(), Backend: resp.GetProvider(),
		ProtocolMajor: resp.GetProtocolMajor(), ProtocolMinor: resp.GetProtocolMinor(), MaxTTL: maxTTL, MaxSandboxes: int(resp.GetMaxSandboxes())}
	p.caps = gatewaywire.CapsFromPB(resp)
	p.caps.Provider = "gateway/" + resp.GetProvider()
	p.caps.Notes = joinNotes(p.caps.Notes, fmt.Sprintf("Sandboxes run on the gateway at %s, which accepts a TTL of at most %v and holds at most %d sandboxes at once.",
		host, maxTTL, resp.GetMaxSandboxes()))
	return p, nil
}

// joinNotes appends sentence to notes as a new sentence.
func joinNotes(notes, sentence string) string {
	notes = strings.TrimRight(strings.TrimSpace(notes), ".")
	if notes == "" {
		return sentence
	}
	return notes + ". " + sentence
}

// parseURL checks rawURL against Dial's rules and returns the host:port to
// dial, the host for TLS, and whether to use TLS.
func parseURL(rawURL string) (target, host string, useTLS bool, err error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", "", false, fmt.Errorf("gateway: URL %q does not parse: %w", rawURL, err)
	}
	refuse := func(what string) (string, string, bool, error) {
		return "", "", false, fmt.Errorf("gateway: URL %q is refused: %s", rawURL, what)
	}
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return refuse(fmt.Sprintf("scheme %q is neither https nor http", u.Scheme))
	case u.Opaque != "":
		return refuse("it has an opaque part instead of //host")
	case u.User != nil:
		return refuse("it carries user information")
	case u.RawQuery != "" || u.ForceQuery:
		return refuse("it carries a query")
	case u.Fragment != "" || u.RawFragment != "":
		return refuse("it carries a fragment")
	case u.Path != "" && u.Path != "/":
		return refuse(fmt.Sprintf("path %q is neither empty nor /", u.Path))
	case u.Hostname() == "":
		return refuse("it names no host")
	}
	host, port := u.Hostname(), u.Port()
	useTLS = u.Scheme == "https"
	if !useTLS && !isLoopback(host) {
		return refuse(fmt.Sprintf("http is accepted only for localhost or a loopback address, and %q is neither; use https", host))
	}
	if port == "" {
		port = "80"
		if useTLS {
			port = "443"
		}
	}
	return net.JoinHostPort(host, port), host, useTLS, nil
}

// isLoopback reports whether host is localhost or a loopback IP literal.
func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	a, err := netip.ParseAddr(host)
	return err == nil && a.IsLoopback()
}

// checkToken refuses a token that is empty or holds a byte outside
// printable ASCII or whitespace. The error never contains the token.
func checkToken(tok string) error {
	if tok == "" {
		return errors.New("gateway: Options.Token is empty: the token must be non-empty printable ASCII without whitespace")
	}
	for i := 0; i < len(tok); i++ {
		if tok[i] < 0x21 || tok[i] > 0x7e {
			return fmt.Errorf("gateway: Options.Token has a byte outside printable ASCII or a space at offset %d: the token must be non-empty printable ASCII without whitespace", i)
		}
	}
	return nil
}

// rpcErr is the error a failed Gateway RPC returns: one matching
// ErrUnauthenticated for a refused token, else one naming the gateway.
func (p *Provider) rpcErr(rpc string, err error) error {
	if status.Code(err) == codes.Unauthenticated {
		return fmt.Errorf("%w: gateway %s refused the token (%s): %s", ErrUnauthenticated, p.url, rpc, status.Convert(err).Message())
	}
	return fmt.Errorf("gateway %s: %s: %w", p.url, rpc, err)
}
