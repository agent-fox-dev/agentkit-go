package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// options are the parsed command line.
type options struct {
	listen       string
	tokenFile    string
	tlsCert      string
	tlsKey       string
	plaintext    bool
	engineBinary string
	engine       string
	runtime      string
	userns       string
	network      []sandbox.NetworkMode
	maxSandboxes int
	maxTTL       time.Duration
	reapInterval time.Duration
	version      bool
}

// newFlagSet declares the command's flags into o. No flag supplies a token:
// tokens come from --token-file only.
func newFlagSet(o *options, network *string) *flag.FlagSet {
	fs := flag.NewFlagSet("agentkit-gateway", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.listen, "listen", "127.0.0.1:8443", "the address to listen on")
	fs.StringVar(&o.tokenFile, "token-file", "", "the file holding the accepted tokens, one per line (required)")
	fs.StringVar(&o.tlsCert, "tls-cert", "", "the server's certificate (PEM); needs --tls-key")
	fs.StringVar(&o.tlsKey, "tls-key", "", "the server's private key (PEM); needs --tls-cert")
	fs.BoolVar(&o.plaintext, "plaintext", false, "serve without TLS; required when the TLS flags are absent")
	fs.StringVar(&o.engineBinary, "engine-binary", "podman", "the container engine command")
	fs.StringVar(&o.engine, "engine", "", "podman or docker; empty means detected")
	fs.StringVar(&o.runtime, "runtime", "", "empty, or krun")
	fs.StringVar(&o.userns, "userns", "", "the user-namespace setting passed to the engine")
	fs.StringVar(network, "network", "none", "comma-separated network modes clients may request: none, unrestricted")
	fs.IntVar(&o.maxSandboxes, "max-sandboxes", 16, "the most sandboxes held at once")
	fs.DurationVar(&o.maxTTL, "max-ttl", 24*time.Hour, "the longest TTL accepted")
	fs.DurationVar(&o.reapInterval, "reap-interval", 10*time.Minute, "how often the backend is reaped")
	fs.BoolVar(&o.version, "version", false, "print the version and exit")
	return fs
}

// parseFlags parses args; any error is a usage error.
func parseFlags(args []string) (options, error) {
	var o options
	var network string
	fs := newFlagSet(&o, &network)
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if o.version {
		return o, nil
	}
	switch {
	case fs.NArg() > 0:
		return o, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	case o.tokenFile == "":
		return o, errors.New("--token-file is required")
	case (o.tlsCert == "") != (o.tlsKey == ""):
		return o, errors.New("--tls-cert and --tls-key go together")
	case o.plaintext && o.tlsCert != "":
		return o, errors.New("--plaintext cannot be combined with --tls-cert and --tls-key")
	case !o.plaintext && o.tlsCert == "":
		return o, errors.New("TLS needs --tls-cert and --tls-key; to serve without TLS, pass --plaintext")
	case o.engine != "" && o.engine != "podman" && o.engine != "docker":
		return o, fmt.Errorf("--engine %q is not podman or docker", o.engine)
	case o.runtime != "" && o.runtime != "krun":
		return o, fmt.Errorf("--runtime %q is not krun", o.runtime)
	case o.maxSandboxes <= 0:
		return o, fmt.Errorf("--max-sandboxes %d is not positive", o.maxSandboxes)
	case o.maxTTL <= 0:
		return o, fmt.Errorf("--max-ttl %v is not positive", o.maxTTL)
	case o.reapInterval <= 0:
		return o, fmt.Errorf("--reap-interval %v is not positive", o.reapInterval)
	}
	for _, m := range strings.Split(network, ",") {
		switch strings.TrimSpace(m) {
		case "none":
			o.network = append(o.network, sandbox.NetworkNone)
		case "unrestricted":
			o.network = append(o.network, sandbox.NetworkUnrestricted)
		default:
			return o, fmt.Errorf("--network entry %q is not none or unrestricted", m)
		}
	}
	return o, nil
}
