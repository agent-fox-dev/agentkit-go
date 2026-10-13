package podman

import (
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
	"unicode"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// checkSpec refuses, before any engine command, what CheckSpec allows but a
// container cannot honour: any Spec.Provider key, a label the provider
// reserves or the engine cannot carry, a Workdir that is not a plain
// absolute path outside /tmp and the kernel's filesystems, an image
// reference that could be read as a flag, and a HOME or TMPDIR that would be
// read-only under --read-only. eff is the effective spec.
func (p *provider) checkSpec(eff sandbox.Spec) error {
	refuse := func(field, reason string) error {
		return &sandbox.SpecError{Provider: string(p.engine), Field: field, Reason: reason}
	}
	if len(eff.Provider) > 0 {
		k := slices.Sorted(maps.Keys(eff.Provider))[0]
		return refuse("Provider."+k, fmt.Sprintf("it defines no Spec.Provider options, and got %q", k))
	}
	for _, k := range slices.Sorted(maps.Keys(eff.Labels)) {
		switch {
		case strings.HasPrefix(k, labelID+"."):
			return refuse("Labels", fmt.Sprintf("the key prefix %q is reserved by the provider, and key %q has it", labelID+".", k))
		case k == "" || strings.Contains(k, "=") || hasSpaceOrControl(k):
			return refuse("Labels", fmt.Sprintf("key %q is empty or contains '=', whitespace or a control character", k))
		case hasControl(eff.Labels[k]):
			return refuse("Labels", fmt.Sprintf("the value of key %q contains a control character", k))
		}
	}
	if reason := badWorkdir(eff.Workdir); reason != "" {
		return refuse("Workdir", fmt.Sprintf("%q %s", eff.Workdir, reason))
	}
	if strings.HasPrefix(eff.Image, "-") || hasSpaceOrControl(eff.Image) {
		return refuse("Image", fmt.Sprintf("%q starts with '-' or contains whitespace or a control character", eff.Image))
	}
	for _, name := range []string{"HOME", "TMPDIR"} {
		v, ok := eff.Env[name]
		if !ok {
			continue
		}
		c := path.Clean(v)
		if !path.IsAbs(v) || !within(c, "/tmp") && !within(c, eff.Workdir) {
			return refuse("Env."+name, fmt.Sprintf("%q is not /tmp, the workspace %s, or beneath either: nothing else is writable under --read-only", v, eff.Workdir))
		}
	}
	return nil
}

// badWorkdir says why w cannot be the workspace mount, or is "".
func badWorkdir(w string) string {
	switch {
	case strings.Contains(w, ",") || hasControl(w):
		return "contains ',' or a control character"
	case !path.IsAbs(w) || path.Clean(w) != w:
		return "is not an absolute, clean path"
	case w == "/":
		return "is the root directory"
	case within(w, "/tmp"):
		return "is /tmp or beneath it, which is the tmpfs"
	case within(w, "/proc") || within(w, "/sys") || within(w, "/dev"):
		return "is beneath /proc, /sys or /dev"
	}
	return ""
}

// within reports whether p is root or beneath it, component-wise.
func within(p, root string) bool { return p == root || strings.HasPrefix(p, root+"/") }

func hasControl(s string) bool { return strings.IndexFunc(s, unicode.IsControl) >= 0 }

func hasSpaceOrControl(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0
}
