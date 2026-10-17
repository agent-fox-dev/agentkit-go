package mcpserve

import "runtime/debug"

// versionFrom is the version of this module that bi records: the main
// module's, when the binary is built from this module, otherwise the
// dependency's, or its replacement's when it is replaced. It is "(devel)" when
// there is none. It is the algorithm of sandbox/internal/buildver, which this
// module cannot import without requiring the sandbox module.
func versionFrom(bi *debug.BuildInfo) string {
	if bi == nil {
		return "(devel)"
	}
	v := ""
	if bi.Main.Path == selfPath {
		v = bi.Main.Version
	} else {
		for _, d := range bi.Deps {
			if d.Path != selfPath {
				continue
			}
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

// moduleVersion is versionFrom for the running binary.
func moduleVersion() string {
	bi, _ := debug.ReadBuildInfo()
	return versionFrom(bi)
}
