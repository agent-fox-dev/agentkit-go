package buildver

import (
	"runtime/debug"
	"testing"
)

// buildInfoVersion is the build-info answer, computed independently of
// Version: the sandbox module's version, its replacement's when replaced, or
// (devel).
func buildInfoVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "(devel)"
	}
	v := ""
	if bi.Main.Path == modulePath {
		v = bi.Main.Version
	}
	for _, d := range bi.Deps {
		if d.Path == modulePath {
			v = d.Version
			if d.Replace != nil {
				v = d.Replace.Version
			}
		}
	}
	if v == "" {
		return "(devel)"
	}
	return v
}

func TestVersionIsTheStampWhenOneIsSet(t *testing.T) {
	saved := stamp
	t.Cleanup(func() { stamp = saved })
	stamp = "v0.6.0"
	if got := Version(); got != "v0.6.0" {
		t.Errorf("with the stamp v0.6.0, Version() = %q", got)
	}
}

func TestVersionFallsBackToTheBuildInfo(t *testing.T) {
	saved := stamp
	t.Cleanup(func() { stamp = saved })
	stamp = ""
	if got, want := Version(), buildInfoVersion(); got != want {
		t.Errorf("with no stamp, Version() = %q, want the build-info answer %q", got, want)
	}
}

func TestBuildReportsTheStampedCommitAndTime(t *testing.T) {
	savedCommit, savedTime := commit, buildTime
	t.Cleanup(func() { commit, buildTime = savedCommit, savedTime })
	commit, buildTime = "abc1234", "2026-10-14T00:00:00Z"
	if c, bt := Build(); c != "abc1234" || bt != "2026-10-14T00:00:00Z" {
		t.Errorf("Build() = %q, %q", c, bt)
	}
	commit, buildTime = "", ""
	if c, bt := Build(); c != "" || bt != "" {
		t.Errorf("with nothing stamped, Build() = %q, %q", c, bt)
	}
}
