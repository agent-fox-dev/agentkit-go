// Package token holds the gateway's bearer-token rules, shared by the
// server library and the agentkit-gateway command.
package token

import (
	"errors"
	"fmt"
)

// MinLen is the shortest token a gateway accepts.
const MinLen = 32

// Validate checks tokens: at least one; each at least MinLen bytes of
// printable ASCII without whitespace; none repeated. An error names a
// token's position, never its value.
func Validate(tokens []string) error {
	if len(tokens) == 0 {
		return errors.New("no token configured: at least one is required")
	}
	seen := map[string]int{}
	for i, t := range tokens {
		if len(t) < MinLen {
			return fmt.Errorf("token %d is %d characters long; at least %d are required", i+1, len(t), MinLen)
		}
		for j := 0; j < len(t); j++ {
			if t[j] < 0x21 || t[j] > 0x7e {
				return fmt.Errorf("token %d has a whitespace or non-printable-ASCII character at offset %d", i+1, j)
			}
		}
		if k, ok := seen[t]; ok {
			return fmt.Errorf("token %d repeats token %d", i+1, k)
		}
		seen[t] = i + 1
	}
	return nil
}
