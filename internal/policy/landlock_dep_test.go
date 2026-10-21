package policy

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// addedBySpec27 are the modules the OpenRouter SDK added to the sandbox
// module's graph, whose go.mod replaces the root module (docs/DEPS.md).
var addedBySpec27 = []string{"github.com/OpenRouterTeam/go-sdk", "github.com/spyzhov/ajson"}

// addedBySpec28 are the modules the Ollama module added to the same graph: the
// module itself and the ones its package api links or its go.mod pulls in
// (docs/DEPS.md).
var addedBySpec28 = []string{
	"github.com/kr/pretty", "github.com/mailru/easyjson", "github.com/ollama/ollama",
	"github.com/rogpeppe/go-internal", "github.com/wk8/go-ordered-map/v2",
}

// addedBySpec29 are the modules that entered the same graph when the root
// package began to import provider/openrouter and provider/ollama: the sandbox
// module compiles the root package in its tests, so its go.mod now requires the
// two SDK modules itself, and listing them there brings in the modules their own
// go.mod files name (docs/DEPS.md, docs/errata/29_provider_selection.md). None
// is compiled into the sandbox agent.
var addedBySpec29 = []string{
	"github.com/TheTitanrain/w32", "github.com/agnivade/levenshtein",
	"github.com/aymanbagabas/go-osc52/v2", "github.com/bytedance/sonic",
	"github.com/bytedance/sonic/loader", "github.com/charmbracelet/bubbletea",
	"github.com/charmbracelet/colorprofile", "github.com/charmbracelet/lipgloss",
	"github.com/charmbracelet/x/ansi", "github.com/charmbracelet/x/cellbuf",
	"github.com/charmbracelet/x/term", "github.com/cloudwego/base64x", "github.com/cloudwego/iasm",
	"github.com/containerd/console", "github.com/d4l3k/go-bfloat16", "github.com/emirpasic/gods/v2",
	"github.com/erikgeiser/coninput", "github.com/gabriel-vasile/mimetype",
	"github.com/gin-contrib/cors", "github.com/gin-contrib/sse", "github.com/gin-gonic/gin",
	"github.com/go-playground/locales", "github.com/go-playground/universal-translator",
	"github.com/go-playground/validator/v10", "github.com/goccy/go-json",
	"github.com/inconshreveable/mousetrap", "github.com/josharian/intern",
	"github.com/json-iterator/go", "github.com/klauspost/compress", "github.com/klauspost/cpuid/v2",
	"github.com/kr/text", "github.com/ledongthuc/pdf", "github.com/leodido/go-urn",
	"github.com/lucasb-eyer/go-colorful", "github.com/mattn/go-isatty",
	"github.com/mattn/go-localereader", "github.com/mattn/go-runewidth",
	"github.com/mattn/go-sqlite3", "github.com/modern-go/concurrent", "github.com/modern-go/reflect2",
	"github.com/muesli/ansi", "github.com/muesli/cancelreader", "github.com/muesli/termenv",
	"github.com/olekukonko/tablewriter", "github.com/pkg/browser", "github.com/rivo/uniseg",
	"github.com/spf13/cobra", "github.com/spf13/pflag", "github.com/tkrajina/go-reflector",
	"github.com/tkrajina/typescriptify-golang-structs", "github.com/twitchyliquid64/golang-asm",
	"github.com/ugorji/go/codec", "github.com/x448/float16", "github.com/xo/terminfo",
	"go.yaml.in/yaml/v3", "golang.org/x/arch", "golang.org/x/image",
}

// TS-15-40: go-landlock is promoted to a direct requirement of the sandbox
// module at its existing version, approved with a reason; psx stays
// indirect and the module graph is 14's, plus the SDK that spec 27 added
// (restated by 27-REQ-9.5 from whole-file equality). 15-REQ-8.1, 15-REQ-8.2. The
// requirement is the nested sandbox module's (14's D-2), and the version is
// the pseudo-version 14 selected, not the v0.10.1 the spec expected
// (docs/errata/15_sandbox_landlock_agent.md).
func TestLandlockPromoted_TS15_40(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	root := repoRoot(t)
	sb := filepath.Join(root, "sandbox")
	snapPath := filepath.Join(root, "internal", "policy", "testdata", "sandbox-go-list-m-all.txt")
	snap, err := os.ReadFile(snapPath)
	if err != nil {
		t.Fatal(err)
	}
	pre, err := os.ReadFile(filepath.Join(root, "internal", "policy", "testdata", "sandbox-go-list-m-all.14.txt"))
	if err != nil {
		t.Fatal(err)
	}
	// Every line 14 committed is still in the snapshot, as upgraded by specs 28
	// and 29 (acceptedLine), and the only lines added since are the OpenRouter SDK
	// and the module it requires (spec 27), the Ollama module's (spec 28,
	// docs/DEPS.md) and the modules their go.mod files name (spec 29): the graph
	// is otherwise 14's.
	have := strings.Split(strings.TrimSpace(string(snap)), "\n")
	var baseline []string
	for _, line := range strings.Split(strings.TrimSpace(string(pre)), "\n") {
		baseline = append(baseline, acceptedLine(line))
	}
	for _, line := range baseline {
		if !slices.Contains(have, line) {
			t.Errorf("%q is missing from the sandbox module's graph snapshot: 14 committed it", line)
		}
	}
	for _, line := range have {
		if f := strings.Fields(line); !slices.Contains(baseline, line) &&
			!slices.Contains(addedBySpec27, f[0]) && !slices.Contains(addedBySpec28, f[0]) && !slices.Contains(addedBySpec29, f[0]) {
			t.Errorf("%q is in the sandbox module's graph snapshot, but 14 did not commit it and specs 27, 28 and 29 did not add it", line)
		}
	}
	want := ""
	for _, line := range strings.Split(string(snap), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "github.com/landlock-lsm/go-landlock" {
			want = f[1]
		}
	}
	reqs := parseGoMod(t, sb)
	if r := reqs["github.com/landlock-lsm/go-landlock"]; want == "" || r.version != want || r.indirect {
		t.Errorf("sandbox/go.mod requires go-landlock %+v, want %s direct", r, want)
	}
	if r := reqs["kernel.org/pub/linux/libs/security/libcap/psx"]; !r.indirect {
		t.Errorf("psx is %+v, want indirect", r)
	}
	if _, ok := parseGoMod(t, root)["github.com/landlock-lsm/go-landlock"]; ok {
		t.Error("the root go.mod requires go-landlock; only the sandbox module may")
	}
	direct14 := []string{"github.com/agent-fox-dev/agentkit-go", "github.com/agent-fox-dev/agentkit-go/sandbox",
		"github.com/gokrazy/rsync", "google.golang.org/grpc", "google.golang.org/protobuf"}
	wantDirect := slices.Sorted(slices.Values(append(direct14, "github.com/landlock-lsm/go-landlock")))
	got := directSet(t, sb)
	if !slices.Equal(got, wantDirect) {
		t.Errorf("the sandbox module's direct set is %v, want 14's plus go-landlock: %v", got, wantDirect)
	}
	if err := validateAllowlist(got, append(slices.Clone(approvedDeps), sandboxModules...)); err != nil {
		t.Error(err)
	}
	if !slices.Contains(approvedDeps, "github.com/landlock-lsm/go-landlock") || !commentedDeps(t, root)["github.com/landlock-lsm/go-landlock"] {
		t.Error("approvedDeps lacks github.com/landlock-lsm/go-landlock with a reason comment above it")
	}
	cmd := exec.Command("go", "list", "-m", "all")
	cmd.Dir = sb
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -m all: %v", err)
	}
	if !bytes.Equal(out, snap) {
		t.Errorf("go list -m all differs from the snapshot:\n%s", out)
	}
}
