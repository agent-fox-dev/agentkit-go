package ollama

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// The ports Ollama's own CLI falls back to: its server's port when the value has
// no scheme, and the scheme's when it has one.
const (
	defaultPort      = "11434"
	defaultHTTPPort  = "80"
	defaultHTTPSPort = "443"
)

// parseHost resolves an OLLAMA_HOST value to the server's base URL, by the
// rules Ollama's own CLI applies, so one value means the same server for the
// ollama command and for AgentKit: the value is trimmed; no scheme means http
// and port 11434; http:// without a port means 80 and https:// without one 443;
// a path is kept. An empty value is DefaultHost.
//
// It is stricter than the CLI where the CLI guesses, and fails with an error
// wrapping ErrInvalidHost: the scheme must be exactly http or https, the port
// must be a number from 1 to 65535 (the CLI uses its default for any other),
// the value must hold an address and no query or fragment, it must carry no user
// information, and it must not be the hosted service.
func parseHost(raw string) (*url.URL, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		s = DefaultHost
	}
	scheme, rest, hasScheme := strings.Cut(s, "://")
	if !hasScheme {
		scheme, rest = "http", s
	}
	if scheme != "http" && scheme != "https" {
		return nil, invalidHost(s, fmt.Sprintf("the scheme is %q, not http or https", scheme))
	}
	if strings.ContainsAny(rest, "?#") {
		return nil, invalidHost(s, "a host is an address and a path, not a URL with a query or a fragment")
	}
	authority, path, _ := strings.Cut(rest, "/")
	if strings.Contains(authority, "@") {
		return nil, invalidHost(s, "it carries user information, and AgentKit sends no credential to Ollama")
	}
	host, port, err := splitAuthority(authority)
	if err != nil {
		return nil, invalidHost(s, err.Error())
	}
	if port, err = resolvePort(port, scheme, hasScheme); err != nil {
		return nil, invalidHost(s, err.Error())
	}
	if name := strings.ToLower(strings.TrimSuffix(host, ".")); name == "ollama.com" || strings.HasSuffix(name, ".ollama.com") {
		return nil, invalidHost(s, "the hosted Ollama service (ollama.com) is not supported; run a local or self-hosted server")
	}
	if host == "" {
		host = "127.0.0.1"
	}
	u := &url.URL{Scheme: scheme, Host: net.JoinHostPort(host, port)}
	if path != "" {
		u.Path = "/" + path
	}
	return u, nil
}

// invalidHost is the error for a host value, with the value shown and its
// password, if it has one, taken out.
func invalidHost(value, why string) error {
	return fmt.Errorf("%w: %s (%s)", ErrInvalidHost, why, redactUserInfo(value))
}

// redactUserInfo returns s with the password of any user information replaced,
// so a value can be shown in an error. It reads everything up to the last @ as
// user information, which hides more than needed for an address with an @ in its
// path and never less than a password.
func redactUserInfo(s string) string {
	start := 0
	if i := strings.Index(s, "://"); i >= 0 {
		start = i + len("://")
	}
	at := strings.LastIndex(s, "@")
	if at < start {
		return s
	}
	user, _, hasPassword := strings.Cut(s[start:at], ":")
	if hasPassword {
		user += ":xxxxx"
	}
	return s[:start] + user + "@" + s[at+1:]
}

// splitAuthority splits the host and port of the part of a value before its
// path. The port is "" when there is none. A host may be a name, an IPv4
// address, or an IPv6 address with or without brackets.
func splitAuthority(authority string) (host, port string, err error) {
	host, port, err = net.SplitHostPort(authority)
	if err != nil {
		// No port: a name, an IP address, or an IPv6 address in brackets.
		host, port = authority, ""
		switch {
		case strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]"):
			host = host[1 : len(host)-1]
			if ip := net.ParseIP(host); ip == nil || !strings.Contains(host, ":") {
				return "", "", fmt.Errorf("%q in brackets is not an IPv6 address", host)
			}
		case net.ParseIP(host) != nil:
		case strings.ContainsAny(host, "[]:"):
			return "", "", fmt.Errorf("%q cannot be parsed as a host and a port", authority)
		}
	}
	if strings.ContainsAny(host, " \t\r\n\\") {
		return "", "", fmt.Errorf("the host %q holds a space or a backslash", host)
	}
	return host, port, nil
}

// resolvePort returns the port to use: the given one when it is a number from 1
// to 65535, and the default of the scheme when there is none.
func resolvePort(port, scheme string, hasScheme bool) (string, error) {
	if port == "" {
		switch {
		case !hasScheme:
			return defaultPort, nil
		case scheme == "https":
			return defaultHTTPSPort, nil
		}
		return defaultHTTPPort, nil
	}
	n, err := strconv.Atoi(port)
	if err != nil || strings.Trim(port, "0123456789") != "" || n < 1 || n > 65535 {
		return "", fmt.Errorf("the port %q is not a number from 1 to 65535", port)
	}
	return strconv.Itoa(n), nil
}
