// Command agentkit-gateway serves the AgentKit sandbox gateway over TCP
// with a Podman or Docker backend: clients (sandbox/gateway) create, use
// and destroy sandboxes on this host with a bearer token read from
// --token-file. It listens on loopback by default and needs TLS
// (--tls-cert and --tls-key) or an explicit --plaintext.
//
// Exit codes: 0 after a clean shutdown or --version; 1 for a startup
// failure or a failed shutdown; 2 for a usage error.
package main

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/agent-fox-dev/agentkit-go/sandbox/podman"
)

func main() {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, deps{newBackend: podman.New, signals: sigs}))
}
