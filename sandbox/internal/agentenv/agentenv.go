// Package agentenv reads the agent's explicit environment: the variables a
// provider handed the agent, never the process's own.
package agentenv

// Value is the last value of key in env, as exec resolves duplicates, or "".
func Value(env []string, key string) string {
	v := ""
	for _, kv := range env {
		if len(kv) > len(key) && kv[len(key)] == '=' && kv[:len(key)] == key {
			v = kv[len(key)+1:]
		}
	}
	return v
}

// TempDir is the explicit environment's temporary directory: TMPDIR, else
// TEMP, else TMP, else /tmp. source names the variable that supplied it, or
// is "" for the /tmp default.
func TempDir(env []string) (dir, source string) {
	for _, k := range []string{"TMPDIR", "TEMP", "TMP"} {
		if v := Value(env, k); v != "" {
			return v, k
		}
	}
	return "/tmp", ""
}
