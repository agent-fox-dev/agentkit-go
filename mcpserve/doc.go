// Package mcpserve serves one agentkit.Runner's tools over MCP to a harness
// that is not AgentKit's own model loop: Claude Code, Pi, or any MCP client
// on the same machine.
//
// It is the one AgentKit module that serves MCP, and it serves one runner's
// tools to a harness on the same machine, over stdio (ServeStdio) or over
// Streamable HTTP on a loopback port behind a bearer token (ServeLoopback).
// Every call runs through the runner's pipeline and guard; the package adds
// only the protocol, and the configuration text a harness needs to connect
// (Endpoint.ClaudeConfig and Endpoint.PiConfig).
//
// It lives in its own module so the root and sandbox modules listen on no
// socket.
package mcpserve
