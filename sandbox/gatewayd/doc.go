// Package gatewayd is the AgentKit sandbox gateway: a gRPC server that
// holds one backend sandbox.Provider and lets authenticated clients
// (sandbox/gateway) create, list and destroy sandboxes with it, relaying the
// Agent protocol to each sandbox it manages.
//
// Every RPC must carry `authorization: Bearer <token>` matching a configured
// token. A sandbox belongs to the token that created it; another token sees
// it as an id that does not exist. The server keeps only each token's
// SHA-256 and logs only its first 8 hex characters.
//
// It is a separate module so the library itself listens on no socket; the
// agentkit-gateway command (cmd/agentkit-gateway) serves it over TCP with a
// Podman or Docker backend.
package gatewayd
