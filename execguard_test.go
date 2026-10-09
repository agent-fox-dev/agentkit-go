package agentkit

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/agentfox/agentkit-go/core"
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
	cfg.ToolPolicy.ExcludeTools = []string{"sub2"}
	ag, err := NewAgent(cfg)
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

// TS-07-11: with no BeforeToolCall, the guard looks through wrappers.
func TestExecuteGuardInspectsReachableTools_TS07_11(t *testing.T) {
	ag, err := NewAgent(agentCfg(shellWrapper()))
	if err != nil {
		t.Fatal(err)
	}
	if err := ag.checkExecuteGuard(); err == nil {
		t.Fatal("a shell tool behind a wrapper passed the guard")
	}
	// Deeper: the shell tool two wrappers down is still found, and the
	// top-level wrapper is the one named.
	outer := core.Tool{Name: "outer", ReachableTools: []core.Tool{shellWrapper()}, Handler: noopHandler}
	ag, err = NewAgent(agentCfg(outer))
	if err != nil {
		t.Fatal(err)
	}
	if err := ag.checkExecuteGuard(); err == nil ||
		!strings.Contains(err.Error(), `wrapper "outer" reached shell tool "execute"`) {
		t.Fatalf("err = %v", err)
	}
}

// TS-07-12: the error wraps ErrUnguardedExecute and names wrapper and shell.
func TestExecuteGuardNamesWrapperAndShell_TS07_12(t *testing.T) {
	ag, err := NewAgent(agentCfg(shellWrapper()))
	if err != nil {
		t.Fatal(err)
	}
	err = ag.checkExecuteGuard()
	if !errors.Is(err, core.ErrUnguardedExecute) {
		t.Fatalf("err = %v, want ErrUnguardedExecute", err)
	}
	if !strings.Contains(err.Error(), `wrapper "code_mode" reached shell tool "execute"`) {
		t.Fatalf("err = %v, want the wrapper and the shell tool named", err)
	}
	// A wrapper the policy reduces to nothing reaches no shell tool.
	cfg := agentCfg(shellWrapper())
	cfg.ToolPolicy.ExcludeTools = []string{"execute"}
	ag, err = NewAgent(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := ag.checkExecuteGuard(); err != nil {
		t.Fatalf("an excluded shell tool still tripped the guard: %v", err)
	}
}

// TS-07-13: with a BeforeToolCall, a reachable shell tool is allowed.
func TestExecuteGuardAllowsInterceptedShell_TS07_13(t *testing.T) {
	cfg := agentCfg(shellWrapper())
	cfg.BeforeToolCall = func(context.Context, core.BeforeToolCallContext) core.BeforeToolCallDecision {
		return core.BeforeToolCallDecision{}
	}
	ag, err := NewAgent(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := ag.checkExecuteGuard(); err != nil {
		t.Fatalf("err = %v, want nil with an interceptor", err)
	}
}
