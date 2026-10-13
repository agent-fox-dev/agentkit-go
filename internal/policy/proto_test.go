package policy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// protoDir is where the agent protocol and its generated code live.
const protoDir = "sandbox/internal/sandboxpb"

// protoFiles are the protocol, its two generated files and the recorded hash,
// relative to the repository root.
var protoFiles = []string{
	protoDir + "/sandbox.proto",
	protoDir + "/sandbox.pb.go",
	protoDir + "/sandbox_grpc.pb.go",
	protoDir + "/sandbox.proto.sha256",
}

// withPath returns the process environment with PATH replaced.
func withPath(path string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "PATH=") {
			env = append(env, kv)
		}
	}
	return append(env, "PATH="+path)
}

// readProtoFiles returns the bytes of every file in protoFiles.
func readProtoFiles(t *testing.T, root string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for _, f := range protoFiles {
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			t.Fatal(err)
		}
		out[f] = b
	}
	return out
}

// TS-13-2: the root module builds, and the sandbox packages vet, with no
// protoc and no protoc plugin on PATH. 13-REQ-1.2.
//
// cgo is off for these builds: with PATH holding only the go binary's
// directory there is no C compiler either, and what is under test is that no
// generator is needed.
func TestBuildNeedsNoProtoc_TS13_2(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the module; skipped under -short")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go is not on PATH")
	}
	goBin, _ = filepath.EvalSymlinks(goBin)
	root := repoRoot(t)
	env := append(withPath(filepath.Dir(goBin)), "CGO_ENABLED=0")
	for _, tool := range []string{"protoc", "protoc-gen-go", "protoc-gen-go-grpc"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(goBin), tool)); err == nil {
			t.Fatalf("%s sits beside the go binary; the test cannot hide it", tool)
		}
	}
	for _, args := range [][]string{{"build", "./..."}, {"vet", "./sandbox/..."}} {
		cmd := exec.Command(goBin, args...)
		cmd.Dir, cmd.Env = root, env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %s with no protoc on PATH: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	for _, f := range protoFiles[1:3] {
		if _, err := os.Stat(filepath.Join(root, f)); err != nil {
			t.Errorf("generated file missing: %v", err)
		}
	}
}

// TS-13-3: make proto installs the pinned plugins into build/bin and
// regenerates output identical to the checked-in code and hash. 13-REQ-1.3.
func TestMakeProtoRegenerates_TS13_3(t *testing.T) {
	if testing.Short() {
		t.Skip("runs make proto; skipped under -short")
	}
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not on PATH")
	}
	root := repoRoot(t)
	dry := exec.Command("make", "-n", "proto")
	dry.Dir = root
	out, err := dry.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n proto: %v\n%s", err, out)
	}
	for _, want := range []string{
		"google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11",
		"google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1",
		"build/bin", "protoc ", protoDir + "/sandbox.proto",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("make -n proto does not mention %q:\n%s", want, out)
		}
	}
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not on PATH")
	}
	before := readProtoFiles(t, root)
	run := exec.Command("make", "proto")
	run.Dir = root
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("make proto: %v\n%s", err, out)
	}
	for _, p := range []string{"protoc-gen-go", "protoc-gen-go-grpc"} {
		if _, err := os.Stat(filepath.Join(root, "build", "bin", p)); err != nil {
			t.Errorf("build/bin lacks %s: %v", p, err)
		}
	}
	after := readProtoFiles(t, root)
	for f, b := range before {
		if !bytes.Equal(b, after[f]) {
			t.Errorf("make proto changed %s: the checked-in file is stale", f)
		}
	}
	sum := sha256.Sum256(after[protoDir+"/sandbox.proto"])
	if got := strings.TrimSpace(string(after[protoDir+"/sandbox.proto.sha256"])); got != hex.EncodeToString(sum[:]) {
		t.Errorf("sandbox.proto.sha256 holds %q, want %x", got, sum)
	}
}

// TS-13-4: make proto without protoc fails, names protoc, and modifies no
// checked-in file. 13-REQ-1.4.
func TestMakeProtoWithoutProtoc_TS13_4(t *testing.T) {
	if testing.Short() {
		t.Skip("runs make proto; skipped under -short")
	}
	bin := t.TempDir()
	for _, tool := range []string{"go", "make"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s is not on PATH", tool)
		}
		if err := os.Symlink(p, filepath.Join(bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	root := repoRoot(t)
	before := readProtoFiles(t, root)
	cmd := exec.Command(filepath.Join(bin, "make"), "proto")
	cmd.Dir, cmd.Env = root, withPath(bin)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("make proto without protoc succeeded:\n%s", out)
	}
	if !strings.Contains(string(out), "protoc") {
		t.Errorf("make proto output does not name protoc:\n%s", out)
	}
	after := readProtoFiles(t, root)
	for f, b := range before {
		if !bytes.Equal(b, after[f]) {
			t.Errorf("make proto without protoc modified %s", f)
		}
	}
}
