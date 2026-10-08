package tools

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"

	"code.dny.dev/ssrf"
)

// This file is REQ-SEC-05: the SSRF guard.
//
// The requirement says the guard validates "at DNS resolution time AND TCP
// connection time", and the conjunction is the whole thing. A guard that
// checks only at resolution has a TOCTOU window a DNS rebind walks straight
// through: the attacker's name resolves to a public address for the check and
// to 169.254.169.254 for the connect, and the SDK fetches the cloud instance
// metadata credentials on their behalf.
//
// The connect-time check is therefore not belt-and-braces. It is the one that
// actually holds, because it runs on the concrete address the kernel is about
// to connect to. The resolution-time check exists to fail fast and to say
// something useful about why.

// ErrBlockedAddress is returned for an address the guard refuses.
var ErrBlockedAddress = errors.New("tools: address is not permitted by the SSRF guard")

// ErrSchemeNotAllowed is REQ-SEC-09: HTTPS only unless HTTP is opted into.
var ErrSchemeNotAllowed = errors.New("tools: only https:// is allowed; set AllowHTTP to permit http://")

// SSRFGuard builds a transport whose every connection is validated.
type SSRFGuard struct {
	// AllowHTTP is REQ-SEC-09's opt-in. Off by default.
	AllowHTTP bool
	// Resolve is injectable so a test can decide what a name resolves to
	// without owning DNS. Nil means net.DefaultResolver.
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
	// DialAddr opens the socket to an ALREADY-VALIDATED address. Nil means a
	// real dialer carrying the connect-time re-check. It is injectable so a
	// test can land a validated public address on a local test server; the
	// validation itself is never injectable.
	DialAddr func(ctx context.Context, network, addr string) (net.Conn, error)
	// TLSClientConfig is passed through to the transport, for a deployment
	// behind a private CA. It does not weaken the guard: the address check
	// happens at dial time, before any handshake.
	TLSClientConfig *tls.Config
	Timeout         time.Duration
}

// Transport returns an http.Transport that dials only through the guard.
//
// Proxies are deliberately NOT honoured: an http_proxy in the environment
// would route every request through a host the guard never validated, which
// silently disables it. A deployment that needs a proxy needs a guard that
// validates the proxy, and that is a different thing than this.
func (g *SSRFGuard) Transport() *http.Transport {
	return &http.Transport{
		Proxy:             nil,
		DialContext:       g.DialContext,
		TLSClientConfig:   g.TLSClientConfig,
		ForceAttemptHTTP2: true,
		// Headers are read into memory before the body cap applies, so
		// without this a server can hand the tool a multi-megabyte header
		// block that no cap ever sees. 64 KiB is well above anything a
		// legitimate response carries.
		MaxResponseHeaderBytes: 64 << 10,
		MaxIdleConns:           8,
		IdleConnTimeout:        30 * time.Second,
		TLSHandshakeTimeout:    10 * time.Second,
		ExpectContinueTimeout:  time.Second,
	}
}

// DialContext validates and connects.
func (g *SSRFGuard) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}

	addrs, err := g.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("tools: %q resolved to no addresses", host)
	}

	// EVERY resolved address must be permitted, not merely the one we would
	// have picked. A name resolving to both a public address and a private one
	// is the rebinding pattern itself, and "connect to the first allowed one"
	// hands the attacker a retry loop.
	for _, a := range addrs {
		if blocked, why := BlockedAddress(a); blocked {
			return nil, fmt.Errorf("%w: %s resolved to %s (%s)", ErrBlockedAddress, host, a, why)
		}
	}

	target := net.JoinHostPort(addrs[0].String(), port)
	if g.DialAddr != nil {
		return g.DialAddr(ctx, network, target)
	}

	d := &net.Dialer{
		Timeout: g.dialTimeout(),
		// Control runs with the concrete address immediately before connect.
		// This is the check that closes the rebinding window; everything above
		// only makes the failure legible.
		Control: func(_, address string, _ syscall.RawConn) error {
			h, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip, err := netip.ParseAddr(h)
			if err != nil {
				return err
			}
			if blocked, why := BlockedAddress(ip); blocked {
				return fmt.Errorf("%w: connect to %s (%s)", ErrBlockedAddress, ip, why)
			}
			return nil
		},
	}
	return d.DialContext(ctx, network, target)
}

func (g *SSRFGuard) dialTimeout() time.Duration {
	if g.Timeout > 0 {
		return g.Timeout
	}
	return 10 * time.Second
}

func (g *SSRFGuard) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip}, nil
	}
	if g.Resolve != nil {
		return g.Resolve(ctx, host)
	}
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// BlockedAddress classifies one address and says why.
//
// The table is code.dny.dev/ssrf's: the IANA special-purpose registries for
// IPv4 and IPv6, and any IPv6 address outside global unicast (2000::/3) —
// loopback, link-local (where the cloud instance metadata endpoint lives),
// private, multicast, NAT64, the deprecated IPv4-compatible ::/96.
//
// An IPv4-MAPPED address is unmapped first. `::ffff:169.254.169.254` routes to
// the IPv4 metadata endpoint, and is judged by the IPv4 table rather than
// refused merely for being outside 2000::/3, so a resolver that hands back
// mapped spellings of public addresses still works.
func BlockedAddress(a netip.Addr) (bool, string) {
	if !a.IsValid() {
		return true, "invalid address"
	}
	if err := ipGuardian.SafeAddr(a.Unmap()); err != nil {
		return true, err.Error()
	}
	return false, ""
}

// ipGuardian checks addresses only: the port and network checks are the
// caller's business (fetch_url dials whatever port the URL names).
var ipGuardian = ssrf.New(ssrf.WithAnyNetwork(), ssrf.WithAnyPort())
