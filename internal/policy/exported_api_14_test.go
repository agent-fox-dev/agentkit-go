package policy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// goldenSHA13 is the SHA-256 of testdata/exported-api.golden as spec 13
// left it (dd2725f), before spec 14's changes.
const goldenSHA13 = "7491419862abf5835c6621fce11fe6efa684c94852a8f09a916b76599fe35b95"

// TS-14-44: the exported identifiers and signatures of agentkit, core,
// tools and codemode still equal the pre-spec golden, and the golden is
// spec 13's. 14-REQ-9.5.
func TestExportedAPIUnchanged_TS14_44(t *testing.T) {
	root := repoRoot(t)
	want, err := os.ReadFile(filepath.Join(root, "internal", "policy", "testdata", "exported-api.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(want); hex.EncodeToString(sum[:]) != goldenSHA13 {
		t.Fatalf("exported-api.golden hashes to %x, not spec 13's %s", sum, goldenSHA13)
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
