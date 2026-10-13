package attach

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/buildver"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/tooldigest"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

// Tools configures the agent's tool set and returns one proxy per
// tools.All tool, in tools.All order, after checking that the agent's
// definitions are the host's. The options are fixed by the first success:
// equal options return the same proxies, different ones ErrToolsConfigured.
func (s *sb) Tools(ctx context.Context, opts sandbox.ToolOptions) ([]core.Tool, error) {
	if err := s.gate(); err != nil {
		return nil, err
	}
	s.toolsMu.Lock()
	defer s.toolsMu.Unlock()
	if s.tools != nil {
		if opts != s.toolOpts {
			return nil, sandbox.ErrToolsConfigured
		}
		return slices.Clone(s.tools), nil
	}
	resp, err := s.client.Tools(s.outgoing(ctx), &sandboxpb.ToolsRequest{
		SymbolsMaxFiles:         int64(opts.Symbols.MaxFiles),
		SymbolsMaxDurationNanos: int64(opts.Symbols.MaxDuration),
		SpillDir:                opts.SpillDir,
		DisableSpill:            opts.DisableSpill,
	})
	if err != nil {
		return nil, s.classify(ctx, "Tools", err)
	}
	host, err := hostTools(opts)
	if err != nil {
		return nil, err
	}
	if diffs := compareTools(resp.Tools, host); len(diffs) > 0 {
		return nil, fmt.Errorf("%w: %s (agent version %s, host version %s)", sandbox.ErrToolMismatch,
			strings.Join(diffs, "; "), s.agent.Version, buildver.Version())
	}
	proxies := make([]core.Tool, len(host))
	for i, h := range host {
		proxies[i] = s.proxy(h)
	}
	s.tools, s.toolOpts = proxies, opts
	return slices.Clone(proxies), nil
}

// hostTools is the host's own tools.All for opts, built over a throwaway
// workspace: only the definitions are used.
func hostTools(opts sandbox.ToolOptions) ([]core.Tool, error) {
	dir, err := os.MkdirTemp("", "agentkit-sandbox-defs-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	ws, err := tools.NewWorkspace(dir)
	if err != nil {
		return nil, err
	}
	return tools.All(tools.Options{Workspace: ws, Env: []string{}, Ignore: tools.NoGlobalExcludes(),
		Symbols: opts.Symbols, SpillDir: opts.SpillDir, DisableSpill: opts.DisableSpill})
}

// compareTools names every tool whose definition differs, that one side
// lacks, or that is out of order.
func compareTools(agent []*sandboxpb.ToolInfo, host []core.Tool) []string {
	at := map[string]int{}
	for i, ti := range agent {
		at[ti.Name] = i
	}
	var diffs []string
	sameSet := len(agent) == len(host)
	for _, h := range host {
		i, ok := at[h.Name]
		switch {
		case !ok:
			diffs = append(diffs, h.Name+" is missing from the agent")
			sameSet = false
		case !bytes.Equal(agent[i].Digest, tooldigest.Digest(h)):
			diffs = append(diffs, h.Name+" has a different definition")
		}
	}
	hs := map[string]bool{}
	for _, h := range host {
		hs[h.Name] = true
	}
	for _, ti := range agent {
		if !hs[ti.Name] {
			diffs = append(diffs, ti.Name+" is not a host tool")
			sameSet = false
		}
	}
	if sameSet {
		for i, h := range host {
			if agent[i].Name != h.Name {
				diffs = append(diffs, fmt.Sprintf("%s is at position %d, not %d", h.Name, at[h.Name], i))
			}
		}
	}
	return diffs
}
