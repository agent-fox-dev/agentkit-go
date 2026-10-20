// Package gatewayd is the AgentKit sandbox gateway: a gRPC server that
// holds one backend sandbox.Provider and lets authenticated clients
// (sandbox/gateway) create, list and destroy sandboxes with it, relaying the
// Agent protocol to each sandbox it manages.
//
// # Authentication
//
// Every RPC must carry `authorization: Bearer <token>`. A Config sets exactly
// one of two ways to decide which tokens are accepted:
//
//   - Static mode, Config.Tokens: a list of tokens, each at least 32
//     characters. The server keeps only each token's SHA-256, compares it with
//     every configured one in constant time, and authenticates each RPC in
//     gRPC's tap handle, before any message is read, and again in its
//     interceptors. The agentkit-gateway command runs in this mode.
//   - Callback mode, Config.Authenticate: the program that embeds the server
//     supplies an Authenticator that turns a token into a principal. The
//     server calls it once for each RPC, from the tap handle, and applies no
//     length or character rule of its own to the token. The Authenticator
//     must be safe for concurrent use and must return quickly without
//     blocking, because a slow one stalls every stream on its connection and
//     the server enforces no timeout. The server trusts it to compare tokens
//     in constant time.
//
// # Principals
//
// An authenticated RPC has a principal. In callback mode it is the string the
// Authenticator returned, 1 to 256 bytes; in static mode it is the
// 64-character lowercase hex SHA-256 of the token. A sandbox belongs to the
// principal of the RPC that created it: any other token of that principal
// reaches it, and a token of another principal sees an id that does not
// exist. Revoking a token refuses its later RPCs; it does not end RPCs in
// flight or remove the principal's sandboxes. A principal must not be a
// secret: callback mode logs it as given and the backend receives it. The
// server never logs a token, and logs only the first 8 characters of a
// static-mode principal.
//
// A Backend learns the principal of the RPC it serves with
// PrincipalFromContext, in Create, in the Sync of a sandbox that a SyncGit RPC
// syncs, and in the Reap that a Gateway.Reap RPC makes. The embedder ends any
// sandbox the server holds, without a client token, with Server.Destroy.
//
// # Gateways in a chain
//
// A Backend may itself be a client of another gateway (sandbox/gateway.Dial),
// which puts this server in front of that one. The sandbox the client returns
// sends agentkit-sandbox-id with its own RPCs, and the relay sends it on with
// each relayed Agent RPC, so the other gateway can route it; the connection
// carries the client's token. Nothing else is passed on: the metadata of the
// caller's RPC is never forwarded, and the agent behind the last gateway sees
// neither the authorization header nor the sandbox id.
//
// It is a separate module so the root and sandbox modules listen on no
// socket; the agentkit-gateway command (cmd/agentkit-gateway) serves it over
// TCP with a Podman or Docker backend.
package gatewayd
