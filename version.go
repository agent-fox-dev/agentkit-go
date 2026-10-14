package agentkit

// Build-time configurable variables, overridable via -ldflags.
var (
	// Version holds the semantic version string.
	Version = "dev"
	// Commit holds the short git commit SHA.
	Commit = "dev"
	// BuildTime holds the UTC build timestamp (e.g. "2025-06-01T00:00:00Z").
	BuildTime = ""
)
