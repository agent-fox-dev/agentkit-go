package local

import (
	"os"
	"path/filepath"
	"slices"
)

// agentEnv is the agent's environment: env, plus PATH, LANG and TERM from
// the host for each one env lacks, plus HOME and TMPDIR pointing at the
// sandbox's private directories, as sorted NAME=value strings.
func agentEnv(env map[string]string, dir string) []string {
	vars := map[string]string{}
	for k, v := range env {
		vars[k] = v
	}
	for _, k := range []string{"PATH", "LANG", "TERM"} {
		if _, ok := vars[k]; !ok {
			if v, ok := os.LookupEnv(k); ok {
				vars[k] = v
			}
		}
	}
	vars["HOME"] = filepath.Join(dir, "home")
	vars["TMPDIR"] = filepath.Join(dir, "tmp")
	out := make([]string, 0, len(vars))
	for k, v := range vars {
		out = append(out, k+"="+v)
	}
	slices.Sort(out)
	return out
}
