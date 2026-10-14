// Package gateway is the client of a remote AgentKit sandbox gateway: Dial
// connects to a gateway (the agentkit-gateway command, or a gatewayd.Server)
// and returns a sandbox.Provider whose sandboxes run on the gateway's host.
// To the embedder and to the model such a sandbox behaves like one created
// locally: the same proxy tools, the same state machine and the same errors.
//
// The gateway holds the backend provider and its credentials; the embedder
// holds only the gateway's URL and bearer token, and the sandbox neither.
package gateway
