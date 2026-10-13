package landlock

import (
	"maps"
	"path/filepath"
	"slices"
)

// defaultPATH is the tools' PATH unless Spec.Env sets one.
const defaultPATH = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// agentEnv is the agent's whole environment: Spec.Env, HOME and TMPDIR set
// to the sandbox's private home/ and tmp/, and defaultPATH unless Spec.Env
// sets PATH, as sorted NAME=value strings. Nothing comes from the embedder.
func agentEnv(specEnv map[string]string, dir string) []string {
	vars := maps.Clone(specEnv)
	if vars == nil {
		vars = map[string]string{}
	}
	vars["HOME"] = filepath.Join(dir, "home")
	vars["TMPDIR"] = filepath.Join(dir, "tmp")
	if _, ok := vars["PATH"]; !ok {
		vars["PATH"] = defaultPATH
	}
	return render(vars)
}

// render is vars as NAME=value strings sorted by name.
func render(vars map[string]string) []string {
	out := make([]string, 0, len(vars))
	for _, k := range slices.Sorted(maps.Keys(vars)) {
		out = append(out, k+"="+vars[k])
	}
	return out
}
