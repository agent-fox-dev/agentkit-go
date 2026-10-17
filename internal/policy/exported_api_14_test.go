package policy

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TS-14-44: the exported identifiers and signatures of every package in
// apiPackages equal the golden. 14-REQ-9.5. The golden is not pinned to spec
// 13's hash: spec 20 (workspace_package) retired the pin, and the golden is the
// change detector over apiPackages (docs/errata/13_sandbox_core.md). This test
// compares the same golden, over the same list, as TS-13-67, TS-15-42 and
// TS-17-61.
func TestExportedAPIUnchanged_TS14_44(t *testing.T) {
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
