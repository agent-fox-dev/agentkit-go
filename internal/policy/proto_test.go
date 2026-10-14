package policy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// protoDir is where the agent protocol and its generated code live.
const protoDir = "sandbox/internal/sandboxpb"

// protoFiles are the agent and gateway protocols, their generated files and
// their recorded hashes, relative to the repository root.
var protoFiles = []string{
	protoDir + "/sandbox.proto",
	protoDir + "/sandbox.pb.go",
	protoDir + "/sandbox_grpc.pb.go",
	protoDir + "/sandbox.proto.sha256",
	protoDir + "/gateway.proto",
	protoDir + "/gateway.pb.go",
	protoDir + "/gateway_grpc.pb.go",
	protoDir + "/gateway.proto.sha256",
}

// generatedProtoFiles are the files make proto generates from protoFiles'
// .proto files.
var generatedProtoFiles = []string{
	protoDir + "/sandbox.pb.go",
	protoDir + "/sandbox_grpc.pb.go",
	protoDir + "/gateway.pb.go",
	protoDir + "/gateway_grpc.pb.go",
}

// checkRecordedHashes fails t for each .proto in files whose recorded
// .sha256 is not its hex SHA-256.
func checkRecordedHashes(t *testing.T, files map[string][]byte) {
	t.Helper()
	for _, name := range []string{"sandbox.proto", "gateway.proto"} {
		sum := sha256.Sum256(files[protoDir+"/"+name])
		if got := strings.TrimSpace(string(files[protoDir+"/"+name+".sha256"])); got != hex.EncodeToString(sum[:]) {
			t.Errorf("%s.sha256 holds %q, want %x", name, got, sum)
		}
	}
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
	// The sandbox tree is its own module (docs/errata/14_sandbox_rsync_sync.md),
	// so its build and vet run there.
	for _, c := range []struct {
		dir  string
		args []string
	}{{".", []string{"build", "./..."}}, {"sandbox", []string{"build", "./..."}}, {"sandbox", []string{"vet", "./..."}}} {
		cmd := exec.Command(goBin, c.args...)
		cmd.Dir, cmd.Env = filepath.Join(root, c.dir), env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %s in %s with no protoc on PATH: %v\n%s", strings.Join(c.args, " "), c.dir, err, out)
		}
	}
	for _, f := range generatedProtoFiles {
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
		"build/bin", "protoc ", protoDir + "/sandbox.proto", protoDir + "/gateway.proto",
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
	checkRecordedHashes(t, after)
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

// unknownProtoc matches make proto's rewritten protoc release header.
var unknownProtoc = regexp.MustCompile(`(?m)^//[[:space:]-]+protoc[[:space:]]+\(unknown\)$`)

// TS-19-6: the policy proto tests list the gateway protocol's four files,
// each generated file carries the (unknown) protoc header, and both recorded
// hashes match their .proto (TS-13-3 regenerates them all with make proto).
// 19-REQ-1.6.
func TestGatewayProtoFiles_TS19_6(t *testing.T) {
	for _, f := range []string{"gateway.proto", "gateway.pb.go", "gateway_grpc.pb.go", "gateway.proto.sha256"} {
		if !slices.Contains(protoFiles, protoDir+"/"+f) {
			t.Errorf("protoFiles does not list %s", f)
		}
	}
	files := readProtoFiles(t, repoRoot(t))
	for _, f := range generatedProtoFiles {
		if !unknownProtoc.Match(files[f]) {
			t.Errorf("%s lacks the (unknown) protoc header", f)
		}
	}
	checkRecordedHashes(t, files)
}
