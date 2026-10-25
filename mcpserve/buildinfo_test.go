package mcpserve

import (
	"runtime/debug"
	"testing"
)

func TestVersionIsThisModulesAsTheBuildRecordsIt(t *testing.T) {
	dep := func(v string, repl *debug.Module) *debug.BuildInfo {
		return &debug.BuildInfo{Main: debug.Module{Path: "example.test/app"},
			Deps: []*debug.Module{{Path: "example.test/other", Version: "v9"}, {Path: selfPath, Version: v, Replace: repl}}}
	}
	for want, infos := range map[string][]*debug.BuildInfo{
		"v1.2.3": {{Main: debug.Module{Path: selfPath, Version: "v1.2.3"}}},
		"v0.4.0": {dep("v0.4.0", nil)},
		"v0.5.0": {dep("v0.0.0", &debug.Module{Path: "../mcpserve", Version: "v0.5.0"})},
		"(devel)": {dep("v0.0.0", &debug.Module{Path: "../mcpserve"}), nil,
			{Main: debug.Module{Path: "example.test/app"}}, {Main: debug.Module{Path: selfPath}}},
	} {
		for _, bi := range infos {
			if got := versionFrom(bi); got != want {
				t.Errorf("versionFrom(%+v) = %q, want %q", bi, got, want)
			}
		}
	}
}
