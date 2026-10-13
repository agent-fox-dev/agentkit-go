// Package localtest is the local provider's test hook: Cut closes the pipe
// connection between the host and a local sandbox's in-process agent, so a
// test can make an in-flight call meet a transport failure. No exported
// method of a public type is added for it.
package localtest

import (
	"fmt"
	"sync"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/attach"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

var (
	mu   sync.Mutex
	cuts = map[string]func() error{}
)

// Register records how to cut the sandbox with the given id. The local
// provider calls it at Create.
func Register(id string, cut func() error) {
	mu.Lock()
	defer mu.Unlock()
	cuts[id] = cut
}

// Deregister forgets the sandbox with the given id.
func Deregister(id string) {
	mu.Lock()
	defer mu.Unlock()
	delete(cuts, id)
}

// Cut closes the connection between the host and sb's agent at once.
func Cut(sb sandbox.Sandbox) error {
	mu.Lock()
	cut, ok := cuts[sb.ID()]
	mu.Unlock()
	if !ok {
		return fmt.Errorf("localtest: %q is not a live local sandbox", sb.ID())
	}
	return cut()
}

// RawClient is a raw protocol client over sb's own connection, for tests that
// send frames the attach code never would.
func RawClient(sb sandbox.Sandbox) sandboxpb.AgentClient {
	return sandboxpb.NewAgentClient(attach.ClientConn(sb))
}
