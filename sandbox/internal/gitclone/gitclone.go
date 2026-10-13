// Package gitclone validates a sandbox.GitRepo and builds the git clone
// argument list a provider's init step runs. sandbox/podman uses it, and so
// will the landlock provider. It imports only the standard library and
// package sandbox, never sandbox/internal/confine.
package gitclone

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// Check refuses a repository the init step must not clone: a URL that is not
// https, has no host, carries user information or a fragment, or holds a
// whitespace or control character; a Ref that starts with '-' or '/',
// contains "..", or holds a character other than an ASCII letter, a digit,
// '.', '_', '-' or '/'; and a negative Depth. Each error names the field.
func Check(repo sandbox.GitRepo) error {
	if err := checkURL(repo.URL); err != nil {
		return err
	}
	if err := checkRef(repo.Ref); err != nil {
		return err
	}
	if repo.Depth < 0 {
		return fmt.Errorf("gitclone: Depth %d is negative", repo.Depth)
	}
	return nil
}

func checkURL(raw string) error {
	refuse := func(reason string) error { return fmt.Errorf("gitclone: URL %q: %s", raw, reason) }
	for _, r := range raw {
		if r < 0x20 || r == 0x7f || unicode.IsSpace(r) || unicode.IsControl(r) {
			return refuse("contains a whitespace or control character")
		}
	}
	if strings.Contains(raw, "#") {
		return refuse("has a fragment")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return refuse(err.Error())
	}
	switch {
	case u.Scheme != "https":
		return refuse("the scheme must be https")
	case u.Hostname() == "":
		return refuse("has no host")
	case u.User != nil:
		return refuse("carries user information; a GitRepo is a public repository")
	case u.Fragment != "":
		return refuse("has a fragment")
	}
	return nil
}

func checkRef(ref string) error {
	refuse := func(reason string) error { return fmt.Errorf("gitclone: Ref %q: %s", ref, reason) }
	switch {
	case ref == "":
		return nil
	case ref[0] == '-' || ref[0] == '/':
		return refuse("starts with '-' or '/'")
	case strings.Contains(ref, ".."):
		return refuse(`contains ".."`)
	}
	for i := 0; i < len(ref); i++ {
		c := ref[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-' || c == '/') {
			return refuse("may hold only ASCII letters, digits, '.', '_', '-' and '/'")
		}
	}
	return nil
}

// Args is the argument list that follows `git` to clone repo into dir:
// `-c protocol.allow=never -c protocol.https.allow=always clone`, then
// `--depth N` when Depth > 0, then `--branch REF` when Ref is set, then
// `--`, the URL and dir. The protocol settings keep every transport but
// https out of reach, even by redirect, and `--` keeps the URL from being
// read as an option. Call Check first.
func Args(repo sandbox.GitRepo, dir string) []string {
	args := []string{"-c", "protocol.allow=never", "-c", "protocol.https.allow=always", "clone"}
	if repo.Depth > 0 {
		args = append(args, "--depth", strconv.Itoa(repo.Depth))
	}
	if repo.Ref != "" {
		args = append(args, "--branch", repo.Ref)
	}
	return append(args, "--", repo.URL, dir)
}
