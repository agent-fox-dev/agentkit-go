package gatewayd

import (
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// The server's log lines. They carry ids, owners, TTLs, network modes, peer
// addresses, reasons, counts and error texts, and never a token, a full
// static-mode token hash, an Env value, a tool input or a tool result.

// ownerLabel is what a log line says of a principal. In static mode the
// principal is a SHA-256 in hex that must not be logged in full, so the label
// is its first 8 characters; in callback mode it is the principal itself.
func (s *Server) ownerLabel(principal string) string {
	if s.authenticator == nil {
		return principal[:min(8, len(principal))]
	}
	return principal
}

func (s *Server) logCreate(id, principal string, ttl time.Duration, mode sandbox.NetworkMode) {
	s.log.Info("sandbox created", "id", id, "owner", s.ownerLabel(principal), "ttl", ttl.String(), "network", mode.String())
}

// destroy reasons.
const (
	reasonClient      = "client"
	reasonExpired     = "expired"
	reasonUnavailable = "unavailable"
	reasonShutdown    = "shutdown"
	reasonEmbedder    = "embedder"
)

func (s *Server) logDestroy(id, reason string, err error) {
	if err != nil {
		s.log.Warn("sandbox destroyed", "id", id, "reason", reason, "error", err.Error())
		return
	}
	s.log.Info("sandbox destroyed", "id", id, "reason", reason)
}

// authentication refusal reasons.
const (
	// reasonNoToken: the authorization header is missing, repeated, not
	// Bearer or empty.
	reasonNoToken = "no token"
	// reasonRefused: no configured token matches, the Authenticator did not
	// accept the token, or an interceptor found no principal.
	reasonRefused = "refused"
	// reasonInvalidPrincipal: the Authenticator accepted the token with an
	// empty principal or one over maxPrincipalLen bytes.
	reasonInvalidPrincipal = "invalid principal"
	// reasonPanic: the Authenticator panicked.
	reasonPanic = "panic"
)

func (s *Server) logAuthRefused(peer, reason string) {
	s.log.Warn("authentication refused", "peer", peer, "reason", reason)
}

func (s *Server) logReap(ids []string, err error) {
	if err != nil {
		s.log.Warn("backend reap", "removed", len(ids), "error", err.Error())
		return
	}
	s.log.Info("backend reap", "removed", len(ids))
}
