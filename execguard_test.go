package agentkit

import (
	"errors"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/guard"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
)

func TestNew_RefusesAShellToolNoGuardAuthorizesBeforeAnyRequest(t *testing.T) {
	excluded := Toolset{Tools: []core.Tool{named("code_mode", named("execute"))}, Policy: core.ToolPolicy{ExcludeTools: []string{"execute"}}}
	for _, c := range []struct {
		name string
		ts   Toolset
		want string // empty: accepted
	}{
		{"execute", Toolset{Tools: []core.Tool{named("execute")}}, core.ErrUnguardedExecute.Error() + ` (tool "execute")`},
		{"run_command", Toolset{Tools: []core.Tool{named("run_command")}}, core.ErrUnguardedExecute.Error() + ` (tool "run_command")`},
		{"behind a wrapper", Toolset{Tools: []core.Tool{named("code_mode", named("execute"))}},
			core.ErrUnguardedExecute.Error() + ` (wrapper "code_mode" reached shell tool "execute")`},
		{"two wrappers down", Toolset{Tools: []core.Tool{named("outer", named("code_mode", named("run_command")))}},
			core.ErrUnguardedExecute.Error() + ` (wrapper "outer" reached shell tool "run_command")`},
		{"with a guard", Toolset{Tools: []core.Tool{named("code_mode", named("execute"))}, Guard: guard.AllowAll}, ""},
		{"excluded by the policy", excluded, ""},
	} {
		fp := faux.New()
		a, err := New(Config{Client: fp, Model: testModel, Toolset: c.ts})
		_, rerr := NewRunner(c.ts)
		switch {
		case c.want == "" && (err != nil || rerr != nil):
			t.Errorf("%s: New = %v, NewRunner = %v; want both to accept it", c.name, err, rerr)
		case c.want != "" && (a != nil || !errors.Is(err, core.ErrUnguardedExecute) || err.Error() != c.want || rerr == nil || rerr.Error() != c.want):
			t.Errorf("%s: New = %v, NewRunner = %v; want %q", c.name, err, rerr, c.want)
		}
		if fp.Calls() != 0 {
			t.Errorf("%s: New sent a request", c.name)
		}
	}
}
