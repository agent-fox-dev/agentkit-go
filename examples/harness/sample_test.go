package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// The sample's spec files carry the headers the format's schemas require,
// and the stand-in specr prints, for any arguments, the envelope of its one task.
func TestTheSampleIsASpecificationPackageThatTheStandInSpecrRenders(t *testing.T) {
	dir := t.TempDir()
	if err := writeSample(dir); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"requirements.json", "test_spec.json", "tasks.json"} {
		var v map[string]any
		if b, err := os.ReadFile(filepath.Join(dir, ".specs", "01_greeting", f)); err != nil || json.Unmarshal(b, &v) != nil ||
			v["spec_id"] != "01" || v["spec_name"] != "greeting" || v["schema_version"] != float64(2) {
			t.Errorf("%s: %v, header %v", f, err, v)
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "greeter.go")); err != nil || !strings.Contains(string(b), `panic("not implemented")`) {
		t.Errorf("greeter.go is not the stub the scripted task edits: %v\n%s", err, b)
	}
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in specr is a shell script")
	}
	bin := filepath.Join(dir, "bin")
	if err := writeStandInSpecr(bin); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(filepath.Join(bin, "specr"), "anything", "--task", "9").Output()
	var env specrResult
	if err != nil || json.Unmarshal(out, &env) != nil {
		t.Fatalf("the stand-in printed no envelope: %v\n%s", err, out)
	}
	if !env.OK || env.Result.SpecDir != ".specs/01_greeting" || env.Result.Task == nil || env.Result.Task.ID != 1 ||
		env.Result.Markdown != sampleRender || !slices.Equal(env.Next[0].Flags, []string{"--task", "1"}) {
		t.Errorf("envelope: %+v", env)
	}
}
