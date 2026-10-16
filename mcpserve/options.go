package mcpserve

import "log/slog"

// Options configures a Server. The zero value is valid.
type Options struct {
	// Name is the server name an MCP client reads in the initialize result.
	// Empty means "agentkit".
	Name string
	// Version is the server version an MCP client reads. Empty means the
	// version of this module in the binary's build information (the
	// replacement's version when the module is replaced), or "(devel)" when
	// there is none.
	Version string
	// Instructions go to the client in the initialize result. A harness shows
	// them to its model, so they are the place for rules about the tools.
	Instructions string
	// Logger receives one record per served call, per refused HTTP request and
	// per stdio session that ends with an error. It never receives the bearer
	// token, a tool's arguments or a result. Nil discards.
	Logger *slog.Logger
}
