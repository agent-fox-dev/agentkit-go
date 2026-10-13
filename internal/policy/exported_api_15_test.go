package policy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// TS-15-42: the exported identifiers and signatures of agentkit, core,
// tools and codemode still equal the golden, and the golden is the one 14
// left (spec 13's: goldenSHA13). The golden records declarations, not doc
// comments, so tools.Workspace's new doc comment does not touch it.
// 15-REQ-8.4.
func TestExportedAPIUnchanged_TS15_42(t *testing.T) {
	root := repoRoot(t)
	want, err := os.ReadFile(filepath.Join(root, "internal", "policy", "testdata", "exported-api.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(want); hex.EncodeToString(sum[:]) != goldenSHA13 {
		t.Fatalf("exported-api.golden hashes to %x, not the pre-spec %s", sum, goldenSHA13)
	}
	var got bytes.Buffer
	for _, pkg := range apiPackages {
		got.WriteString("== " + pkg + "\n")
		for _, line := range exportedAPI(t, filepath.Join(root, pkg)) {
			got.WriteString(line + "\n")
		}
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Errorf("the exported API differs from the golden:\n%s", got.String())
	}
}
