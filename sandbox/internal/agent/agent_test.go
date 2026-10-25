package agent

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
)

// New refuses a missing or non-directory workspace naming it, and a TTL that
// is not positive.
func TestNewRefusesABadConfig(t *testing.T) {
	dir := testutil.Tree(t, map[string]string{"file": "x"})
	for want, cfg := range map[string]Config{
		filepath.Join(dir, "nope"): {Workdir: filepath.Join(dir, "nope"), TTL: time.Hour},
		filepath.Join(dir, "file"): {Workdir: filepath.Join(dir, "file"), TTL: time.Hour},
		"TTL":                      {Workdir: dir, TTL: 0},
	} {
		if a, err := New(cfg); a != nil || err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("New(%+v) = %v, %v; want nil and an error naming %s", cfg, a, err, want)
		}
	}
}
