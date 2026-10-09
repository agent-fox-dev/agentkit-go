package agentkit

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
)

func shellWrapper() core.Tool {
	shell := core.Tool{Name: "execute", Handler: noopHandler}
	return core.Tool{Name: "code_mode", ReachableTools: []core.Tool{shell}, Handler: noopHandler}
}

// TS-07-7: Agent.ReachableTools is the closure of the policy-resolved set.
func TestAgentReachableToolsResolvesPolicy_TS07_7(t *testing.T) {
	sub1 := core.Tool{Name: "sub1", Handler: noopHandler}
	sub2 := core.Tool{Name: "sub2", Handler: noopHandler}
	wrap := core.Tool{Name: "wrap", ReachableTools: []core.Tool{sub1, sub2}, Handler: noopHandler}
	leaf := core.Tool{Name: "leaf", Handler: noopHandler}
	cfg := agentCfg(wrap, leaf)
	cfg.Policy.ExcludeTools = []string{"sub2"}
	ag, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tl := range ag.ReachableTools() {
		got = append(got, tl.Name)
	}
	if strings.Join(got, ",") != "wrap,sub1,leaf" {
		t.Fatalf("ReachableTools = %v, want [wrap sub1 leaf]", got)
	}
}

// TS-07-11: with no Guard, New looks through wrappers.
func TestExecuteGuardInspectsReachableTools_TS07_11(t *testing.T) {
	if _, err := New(agentCfg(shellWrapper())); err == nil {
		t.Fatal("a shell tool behind a wrapper passed the guard")
	}
	// Deeper: the shell tool two wrappers down is still found, and the
	// top-level wrapper is the one named.
	outer := core.Tool{Name: "outer", ReachableTools: []core.Tool{shellWrapper()}, Handler: noopHandler}
	if _, err := New(agentCfg(outer)); err == nil ||
		!strings.Contains(err.Error(), `wrapper "outer" reached shell tool "execute"`) {
		t.Fatalf("err = %v", err)
	}
}

// TS-07-12: the error wraps ErrUnguardedExecute and names wrapper and shell.
func TestExecuteGuardNamesWrapperAndShell_TS07_12(t *testing.T) {
	_, err := New(agentCfg(shellWrapper()))
	if !errors.Is(err, core.ErrUnguardedExecute) {
		t.Fatalf("err = %v, want ErrUnguardedExecute", err)
	}
	if !strings.Contains(err.Error(), `wrapper "code_mode" reached shell tool "execute"`) {
		t.Fatalf("err = %v, want the wrapper and the shell tool named", err)
	}
	// A wrapper the policy reduces to nothing reaches no shell tool.
	cfg := agentCfg(shellWrapper())
	cfg.Policy.ExcludeTools = []string{"execute"}
	if _, err := New(cfg); err != nil {
		t.Fatalf("an excluded shell tool still tripped the guard: %v", err)
	}
}

// TS-07-13: with a Guard, a reachable shell tool is allowed.
func TestExecuteGuardAllowsInterceptedShell_TS07_13(t *testing.T) {
	cfg := agentCfg(shellWrapper())
	cfg.Guard = func(context.Context, core.BeforeToolCallContext) core.BeforeToolCallDecision {
		return core.BeforeToolCallDecision{}
	}
	if _, err := New(cfg); err != nil {
		t.Fatalf("err = %v, want nil with an interceptor", err)
	}
}
