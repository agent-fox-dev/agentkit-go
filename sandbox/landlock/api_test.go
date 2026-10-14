package landlock_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/landlock"
)

var _ func(landlock.Options) (sandbox.Provider, error) = landlock.New

// TS-18-1: Options, ErrNoLandlock and New have the specified shapes, and the
// package builds and vets on linux/amd64, linux/arm64, darwin/arm64 and
// windows/amd64. 18-REQ-1.1.
func TestAPI_TS18_1(t *testing.T) {
	_ = landlock.Options{Root: "", AgentBinary: "", ExtraRO: nil, ExtraRW: nil}
	typ := reflect.TypeFor[landlock.Options]()
	want := []struct {
		name string
		typ  reflect.Type
	}{{"Root", reflect.TypeFor[string]()}, {"AgentBinary", reflect.TypeFor[string]()}, {"ExtraRO", reflect.TypeFor[[]string]()}, {"ExtraRW", reflect.TypeFor[[]string]()}}
	if typ.NumField() != len(want) {
		t.Fatalf("Options has %d fields, want 4", typ.NumField())
	}
	for i, w := range want {
		if f := typ.Field(i); f.Name != w.name || f.Type != w.typ {
			t.Errorf("field %d is %s %s, want %s %s", i, f.Name, f.Type, w.name, w.typ)
		}
	}
	if got := landlock.ErrNoLandlock.Error(); got != "landlock: this host cannot confine a sandbox with Landlock ABI 4" {
		t.Errorf("ErrNoLandlock = %q", got)
	}
	if testing.Short() {
		t.Skip("the cross-target builds are skipped under -short")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go is not on PATH")
	}
	module, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range [][2]string{{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "arm64"}, {"windows", "amd64"}} {
		for _, verb := range []string{"build", "vet"} {
			cmd := exec.Command(goBin, verb, "./landlock/")
			cmd.Dir = module
			cmd.Env = append(os.Environ(), "GOOS="+target[0], "GOARCH="+target[1], "CGO_ENABLED=0")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("GOOS=%s GOARCH=%s go %s ./landlock/: %v\n%s", target[0], target[1], verb, err, out)
			}
		}
	}
}
