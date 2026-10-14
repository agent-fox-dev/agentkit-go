package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/token"
)

// readTokenFile reads the accepted tokens: one per line, surrounding
// whitespace trimmed, blank lines and lines starting with # skipped. The
// gateway's token rules apply; an error never contains a token.
func readTokenFile(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("token file: %w", err)
	}
	var toks []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		toks = append(toks, line)
	}
	if err := token.Validate(toks); err != nil {
		return nil, fmt.Errorf("token file %s: %w", path, err)
	}
	return toks, nil
}
