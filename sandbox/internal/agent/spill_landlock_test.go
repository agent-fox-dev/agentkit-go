package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// TS-15-29: under a ruleset, a Tools SpillDir outside the writable directory
// roots is InvalidArgument naming the path and the roots, and the default
// spill directory always qualifies. 15-REQ-5.2, 15-REQ-5.3.
func TestSpillDirUnderLandlock_TS15_29(t *testing.T) {
	base := t.TempDir()
	w, tmp, o := filepath.Join(base, "w"), filepath.Join(base, "t"), filepath.Join(base, "o")
	for _, d := range []string{w, tmp, o} {
		writeFile(t, d, ".keep", "")
	}
	agentWith := func(l sandbox.LandlockInfo) sandboxpb.AgentClient {
		a, err := New(Config{Workdir: w, Env: []string{"TMPDIR=" + tmp}, TTL: time.Hour, Landlock: l})
		if err != nil {
			t.Fatal(err)
		}
		c, _ := serveRaw(t, a)
		return c
	}
	confined := sandbox.LandlockInfo{ABI: 4, TCP: true, Writable: []string{w, tmp, "/dev/null"}}
	tools := func(c sandboxpb.AgentClient, spill string) error {
		_, err := c.Tools(context.Background(), &sandboxpb.ToolsRequest{SpillDir: spill})
		return err
	}
	c := agentWith(confined)
	for _, spill := range []string{o, w + "-evil", "/dev/null/spill"} {
		err := tools(c, spill)
		msg := status.Convert(err).Message()
		if status.Code(err) != codes.InvalidArgument || !strings.Contains(msg, spill) || !strings.Contains(msg, w) || !strings.Contains(msg, tmp) {
			t.Errorf("SpillDir %s: %v, want InvalidArgument naming it, %s and %s", spill, err, w, tmp)
		}
	}
	if err := tools(c, ""); err != nil {
		t.Errorf("no SpillDir after the refusals: %v", err)
	}
	if err := tools(agentWith(confined), filepath.Join(w, "spill")); err != nil {
		t.Errorf("SpillDir W/spill: %v", err)
	}
	if err := tools(agentWith(sandbox.LandlockInfo{ABI: 4, Writable: []string{w}}), ""); err != nil {
		t.Errorf("no SpillDir with the temporary directory skipped: %v", err)
	}
	if err := tools(agentWith(sandbox.LandlockInfo{}), o); err != nil {
		t.Errorf("SpillDir O at ABI 0: %v", err)
	}
}
