// Package sandbox runs AgentKit's built-in tools inside a sandbox: an
// ephemeral execution environment that holds exactly one workspace.
//
// A Provider creates a Sandbox from a Spec. Sandbox.Sync puts files into its
// workspace, and Sandbox.Tools returns a []core.Tool that is a drop-in
// replacement for tools.All: one proxy for each built-in, with the same
// definition, whose Execute runs the call in the AgentKit agent
// (agentkit-sandboxd) inside the sandbox over the agentkit.sandbox.v1 gRPC
// protocol. The driver does not change and does not know a tool is remote;
// containment comes from where the tools run.
//
// CheckSpec is the one place a Spec is checked against a provider's
// Capabilities; every provider calls it first in Create.
//
// The local provider (sandbox/local) runs the agent in the embedder's own
// process. It is for tests and development and is not isolation of any kind.
package sandbox
