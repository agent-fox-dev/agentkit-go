package gatewayd

import (
	"encoding/hex"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// The server's log lines. They carry ids, the owner's fingerprint, TTLs,
// network modes, peer addresses, reasons, counts and error texts, and never
// a token, a full token hash, an Env value, a tool input or a tool result.

// fingerprint is the first 8 hex characters of an owner.
func (o owner) fingerprint() string { return hex.EncodeToString(o[:4]) }

func (s *Server) logCreate(id string, o owner, ttl time.Duration, mode sandbox.NetworkMode) {
	s.log.Info("sandbox created", "id", id, "owner", o.fingerprint(), "ttl", ttl.String(), "network", mode.String())
}

// destroy reasons.
const (
	reasonClient      = "client"
	reasonExpired     = "expired"
	reasonUnavailable = "unavailable"
	reasonShutdown    = "shutdown"
)

func (s *Server) logDestroy(id, reason string, err error) {
	if err != nil {
		s.log.Warn("sandbox destroyed", "id", id, "reason", reason, "error", err.Error())
		return
	}
	s.log.Info("sandbox destroyed", "id", id, "reason", reason)
}

func (s *Server) logAuthRefused(peer string) {
	s.log.Warn("authentication refused", "peer", peer)
}

func (s *Server) logReap(ids []string, err error) {
	if err != nil {
		s.log.Warn("backend reap", "removed", len(ids), "error", err.Error())
		return
	}
	s.log.Info("backend reap", "removed", len(ids))
}
