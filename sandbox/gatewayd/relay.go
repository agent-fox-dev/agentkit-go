package gatewayd

import "github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"

// relay is the Agent service the gateway serves: it relays each RPC to the
// agent of the sandbox the RPC names.
type relay struct {
	sandboxpb.UnimplementedAgentServer
	s *Server
}
