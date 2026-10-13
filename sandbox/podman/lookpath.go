package podman

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// lookPath resolves binary through the PATH of env, never the process's own
// PATH. A binary with a path separator is checked as it is.
func lookPath(binary string, env []string) (string, error) {
	if strings.ContainsRune(binary, '/') || strings.ContainsRune(binary, filepath.Separator) {
		if executable(binary) {
			return binary, nil
		}
	} else {
		for _, dir := range filepath.SplitList(envValue(env, "PATH")) {
			if dir == "" || !filepath.IsAbs(dir) {
				continue
			}
			for _, name := range candidates(binary) {
				if p := filepath.Join(dir, name); executable(p) {
					return p, nil
				}
			}
		}
	}
	return "", fmt.Errorf("podman: engine binary %q not found on PATH; install podman or docker, or set Options.Binary", binary)
}

// candidates are the file names binary may have: on Windows, also with .exe.
func candidates(binary string) []string {
	if runtime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(binary), ".exe") {
		return []string{binary + ".exe", binary}
	}
	return []string{binary}
}

// executable reports whether p is a regular file that may be executed.
func executable(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular() && (runtime.GOOS == "windows" || fi.Mode()&0o111 != 0)
}

// envValue is the value of name in env, the last one winning as it does for
// exec.Cmd.Env.
func envValue(env []string, name string) string {
	v := ""
	for _, kv := range env {
		if k, val, ok := strings.Cut(kv, "="); ok && (k == name || runtime.GOOS == "windows" && strings.EqualFold(k, name)) {
			v = val
		}
	}
	return v
}
