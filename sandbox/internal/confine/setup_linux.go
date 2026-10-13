//go:build linux

package confine

import (
	"strings"

	"github.com/landlock-lsm/go-landlock/landlock"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// Setup refuses dangerous flag values with a *UsageError, then probes the
// kernel once, and when Plan chooses an ABI applies a
// ruleset of exactly that version to the whole process: Restrict when it
// handles TCP, RestrictPaths otherwise, never BestEffort. Applying sets
// no_new_privs on every thread. A failed probe is no error: the outcome is
// ABI 0 with Plan's note. A ruleset that cannot be built or applied after a
// good probe is an error, and the caller must not serve.
func Setup(in Inputs) (sandbox.LandlockInfo, error) {
	if err := validate(in); err != nil {
		return sandbox.LandlockInfo{}, err
	}
	info, d := decide(in.Mode)
	if d.ABI == 0 {
		return info, nil
	}
	p, err := buildRules(in, d.ABI)
	if err != nil {
		return sandbox.LandlockInfo{}, err
	}
	rules := make([]landlock.Rule, len(p.rules))
	for i, r := range p.rules {
		rules[i] = landlock.PathAccess(landlock.AccessFSSet(r.access), r.path)
	}
	cfg := map[int]landlock.Config{2: landlock.V2, 3: landlock.V3, 4: landlock.V4}[d.ABI]
	if d.TCP {
		err = cfg.Restrict(rules...)
	} else {
		err = cfg.RestrictPaths(rules...)
	}
	if err != nil {
		return sandbox.LandlockInfo{}, err
	}
	info.Writable, info.ReadOnly = p.writable, p.readOnly
	info.Note = strings.Join(append(nonEmpty(d.Note), p.notes...), "; ")
	return info, nil
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}
