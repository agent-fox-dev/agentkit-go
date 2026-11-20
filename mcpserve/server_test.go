package mcpserve

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"slices"
	"strings"
	"testing"

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
	"github.com/agent-fox-dev/agentkit-go/runner"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

// snapshotRunner is a toolRunner, that serves no call, whose tools a test can change after New.
type snapshotRunner struct {
	toolRunner
	tools []core.Tool
	reads int
}

func (r *snapshotRunner) Tools() []core.Tool { r.reads++; return r.tools }

func TestASessionSeesTheServerInfoAndTheToolsAsTheyWereAtNew(t *testing.T) {
	version := "(devel)"
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Path == selfPath && bi.Main.Version != "" {
		version = bi.Main.Version
	}
	bare := tool("bare", nil)
	bare.InputSchema = nil
	for _, c := range []struct {
		opts, info Options
		tools      []core.Tool
		listing    string
	}{
		{Options{}, Options{Name: "agentkit", Version: version}, nil, ""},
		{Options{Name: "n", Version: "v", Instructions: "i"}, Options{Name: "n", Version: "v", Instructions: "i"}, []core.Tool{echoTool(), bare},
			`bare: the bare tool {"type":"object"} true` + "\n" + `echo: the echo tool {"properties":{"text":{"type":"string"}},"type":"object"} true`},
	} {
		r := &snapshotRunner{tools: c.tools}
		s := must(newServer(r, c.opts))
		if r.tools = nil; len(c.tools) > 0 {
			c.tools[0].Description = "changed"
		}
		conn := serveStdio(t, context.Background(), s).conn(t)
		res := must(conn.Session(context.Background())).InitializeResult()
		var got []string
		for _, l := range must(conn.ListTools(context.Background())) { // true: no output schema, annotations or _meta
			got = append(got, fmt.Sprintf("%s: %s %s %v", l.Name, l.Description, testutil.MustJSON(t, l.InputSchema), l.OutputSchema == nil && l.Annotations == nil && l.Meta == nil))
		}
		if info := (Options{Name: res.ServerInfo.Name, Version: res.ServerInfo.Version, Instructions: res.Instructions}); info != c.info {
			t.Errorf("Options %+v: initialize reports %+v, want %+v", c.opts, info, c.info)
		}
		if slices.Sort(got); strings.Join(got, "\n") != c.listing || r.reads != 1 {
			t.Errorf("listed %q after %d reads of Tools; want %q after one", got, r.reads, c.listing)
		}
	}
}

// New takes the runner package's Runner, which is also what the root
// package's NewRunner and an agent's Runner method return: a program that
// built its runner either way serves it unchanged.
func TestNewServesARunnerFromThePackageTheRootOrAnAgent(t *testing.T) {
	ts := runner.Toolset{Tools: []core.Tool{echoTool(), tool("other", nil)}}
	agent := must(agentkit.New(agentkit.Config{Client: faux.New(), Model: "test-model", Toolset: ts}))
	for name, r := range map[string]*runner.Runner{
		"runner.New":         must(runner.New(ts)),
		"agentkit.NewRunner": must(agentkit.NewRunner(ts)),
		"Agent.Runner":       agent.Runner(),
	} {
		s, err := New(r, Options{})
		if err != nil {
			t.Errorf("%s: New = %v", name, err)
			continue
		}
		var got []string
		for _, l := range must(serveStdio(t, context.Background(), s).conn(t).ListTools(context.Background())) {
			got = append(got, l.Name)
		}
		if slices.Sort(got); !slices.Equal(got, []string{"echo", "other"}) {
			t.Errorf("%s: the session lists %v, want echo and other", name, got)
		}
	}
}

func TestNewRefusesANilRunnerOrAToolWhoseSchemaCannotBeServed(t *testing.T) {
	if s, err := New(nil, Options{}); s != nil || err == nil || err.Error() != "mcpserve: Runner is nil" {
		t.Errorf("New(nil) = %v, %v", s, err)
	}
	for name, in := range map[string]*schema.Schema{
		"does not marshal": {Type: schema.TypeObject, Extra: schema.Extra{{Key: "x", Value: json.RawMessage("{")}}},
		"has no type":      {},
		"is an array":      schema.Array(schema.String()),
		"is nullable":      {Type: schema.TypeObject, Nullable: true},
	} {
		bad := tool("bad", nil)
		bad.InputSchema = in
		if s, err := New(runnerOf(bad), Options{}); s != nil || err == nil || !strings.HasPrefix(err.Error(), `mcpserve: tool "bad": input schema`) {
			t.Errorf("a schema that %s: New = %v, %v", name, s, err)
		}
	}
}
