package mcpserve

// An Endpoint is a running loopback MCP endpoint: where a harness connects, and
// the bearer token it must present. ServeLoopback returns it; a harness is told
// of it with ClaudeConfig or PiConfig.
type Endpoint struct {
	// URL is the Streamable HTTP endpoint, http://127.0.0.1:<port>/mcp.
	URL string
	// Token is the bearer token every request must carry, in the header
	// "Authorization: Bearer <Token>": 48 lowercase hex characters, fresh for
	// each endpoint.
	Token string

	stop func()
}

// Close stops the endpoint, as the end of the ctx it was served with does: it
// stops accepting connections, lets requests in flight finish for up to two
// seconds, then closes the connections that remain and cancels the calls still
// running. It returns when that is done. It is safe to call more than once and
// from several goroutines, and it does nothing on an Endpoint that ServeLoopback
// did not return. It returns nil.
func (e *Endpoint) Close() error {
	if e != nil && e.stop != nil {
		e.stop()
	}
	return nil
}
