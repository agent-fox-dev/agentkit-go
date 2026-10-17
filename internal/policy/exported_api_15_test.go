package policy

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TS-15-42: the exported identifiers and signatures of every package in
// apiPackages equal the golden. The golden records declarations, not doc
// comments, so a change to a doc comment does not touch it. 15-REQ-8.4. The
// golden is the change detector over apiPackages, not pinned to spec 13's hash
// (see TS-14-44).
func TestExportedAPIUnchanged_TS15_42(t *testing.T) {
	root := repoRoot(t)
	want, err := os.ReadFile(filepath.Join(root, "internal", "policy", "testdata", "exported-api.golden"))
	if err != nil {
		t.Fatal(err)
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
