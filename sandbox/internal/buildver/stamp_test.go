package buildver

import (
	"runtime/debug"
	"testing"
)

// oracle is the build-info answer, computed independently of Version: the
// sandbox module's version (the module buildver lives in since spec 14's
// D-2), its replacement's when replaced, or (devel).
func oracle() string {
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

// TS-16-1: Version returns the stamp when it is set, and the unchanged
// build-info answer once it is cleared. 16-REQ-4.2, 16-REQ-4.3.
func TestStamp_TS16_1(t *testing.T) {
	saved := stamp
	t.Cleanup(func() { stamp = saved })
	stamp = "v0.6.0"
	if got := Version(); got != "v0.6.0" {
		t.Errorf("with the stamp v0.6.0, Version() = %q", got)
	}
	stamp = ""
	if got, want := Version(), oracle(); got != want {
		t.Errorf("with no stamp, Version() = %q, want the build-info answer %q", got, want)
	}
}

// Build returns the link-time commit and build time, and empty strings when
// the build set none.
func TestBuild(t *testing.T) {
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
