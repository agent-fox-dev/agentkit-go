package confine

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/agentenv"
)

// Inputs are what the ruleset is built from: the workspace, the agent's
// explicit environment (for HOME and the temporary directory), the
// --landlock-ro and --landlock-rw paths and the network mode.
type Inputs struct {
	Workdir string
	Env     []string
	RO, RW  []string
	Mode    sandbox.NetworkMode
}

// access is a set of Landlock filesystem rights, bit for bit the kernel's.
type access uint64

const (
	accExecute access = 1 << iota
	accWriteFile
	accReadFile
	accReadDir
	accRemoveDir
	accRemoveFile
	accMakeChar
	accMakeDir
	accMakeReg
	accMakeSock
	accMakeFifo
	accMakeBlock
	accMakeSym
	accRefer
	accTruncate
)

// fullFS is every filesystem right Landlock ABI abi defines: those of ABI 1,
// refer from ABI 2 and truncate from ABI 3.
func fullFS(abi int) access {
	a := accExecute | accWriteFile | accReadFile | accReadDir | accRemoveDir | accRemoveFile | accMakeChar |
		accMakeDir | accMakeReg | accMakeSock | accMakeFifo | accMakeBlock | accMakeSym
	if abi >= 2 {
		a |= accRefer
	}
	if abi >= 3 {
		a |= accTruncate
	}
	return a
}

// The rights of the read-only roots, of /proc and of the device files.
const (
	readExec = accReadFile | accReadDir | accExecute
	readOnly = accReadFile | accReadDir
	deviceRW = accReadFile | accWriteFile
)

// systemDirs and deviceFiles are the read-execute roots and the device
// nodes every ruleset grants; tests extend them.
var (
	systemDirs  = []string{"/usr", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/libx32", "/etc", "/opt"}
	deviceFiles = []string{"/dev/null", "/dev/zero", "/dev/full", "/dev/random", "/dev/urandom"}
)

// rule grants access beneath path, a directory, or on path, a file (the
// device files carry only file rights).
type rule struct {
	path   string
	access access
}

// plan is a built ruleset: its rules in the order they were added, the
// resolved paths the report lists, and notes on what was skipped.
type plan struct {
	rules              []rule
	writable, readOnly []string
	notes              []string
}

// buildRules resolves every root through symlinks and returns the rules for
// applied ABI abi, in this order: the writable roots (the workspace, HOME,
// the temporary directory, each --landlock-rw), the system directories and
// each --landlock-ro, /proc, and the device files. A root that resolves to
// one already added collapses into it. A missing system or device path is
// skipped silently and a missing flag path with a note; an unusable HOME or
// temporary directory is skipped with a note; any other resolution error is
// returned, naming the path.
func buildRules(in Inputs, abi int) (plan, error) {
	var p plan
	seen := map[string]bool{}
	add := func(path string, acc access, list *[]string) {
		if seen[path] {
			return
		}
		seen[path] = true
		p.rules = append(p.rules, rule{path: path, access: acc})
		*list = append(*list, path)
	}
	// resolve is path with symlinks resolved; ok is false when it is missing.
	resolve := func(path string) (string, bool, error) {
		r, err := filepath.EvalSymlinks(path)
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		if err != nil {
			return "", false, fmt.Errorf("resolving %s: %w", path, err)
		}
		return r, true, nil
	}
	full := fullFS(abi)

	absW, err := filepath.Abs(in.Workdir)
	if err != nil {
		return plan{}, fmt.Errorf("the workspace %s: %w", in.Workdir, err)
	}
	w, ok, err := resolve(absW)
	if err != nil {
		return plan{}, err
	}
	if !ok {
		return plan{}, fmt.Errorf("the workspace %s does not exist", in.Workdir)
	}
	add(w, full, &p.writable)
	tmp, tmpSource := agentenv.TempDir(in.Env)
	if tmpSource == "" {
		tmpSource = "the default temporary directory"
	}
	for _, v := range []struct{ name, value string }{{"HOME", agentenv.Value(in.Env, "HOME")}, {tmpSource, tmp}} {
		r, why, err := envRoot(v.value)
		if err != nil {
			return plan{}, fmt.Errorf("%s=%s: %w", v.name, v.value, err)
		}
		if why != "" {
			p.notes = append(p.notes, fmt.Sprintf("%s=%q %s; not writable", v.name, v.value, why))
			continue
		}
		add(r, full, &p.writable)
	}
	flagRoots := func(flag string, paths []string, acc access, list *[]string) error {
		for _, path := range paths {
			r, ok, err := resolve(path)
			if err != nil {
				return err
			}
			if !ok {
				p.notes = append(p.notes, fmt.Sprintf("%s %s does not exist; skipped", flag, path))
				continue
			}
			add(r, acc, list)
		}
		return nil
	}
	if err := flagRoots("--landlock-rw", in.RW, full, &p.writable); err != nil {
		return plan{}, err
	}
	var devices []string
	silent := func(paths []string, acc access, list *[]string) error {
		for _, path := range paths {
			r, ok, err := resolve(path)
			if err != nil {
				return err
			}
			if ok {
				add(r, acc, list)
			}
		}
		return nil
	}
	if err := silent(systemDirs, readExec, &p.readOnly); err != nil {
		return plan{}, err
	}
	if err := flagRoots("--landlock-ro", in.RO, readExec, &p.readOnly); err != nil {
		return plan{}, err
	}
	if err := silent([]string{"/proc"}, readOnly, &p.readOnly); err != nil {
		return plan{}, err
	}
	dev := deviceRW
	if abi >= 3 {
		dev |= accTruncate // shells open devices with O_TRUNC
	}
	if err := silent(deviceFiles, dev, &devices); err != nil {
		return plan{}, err
	}
	p.writable = append(p.writable, devices...)
	return p, nil
}

// envRoot checks HOME or the temporary directory: it returns the resolved
// root, or why it is skipped (unset, relative, missing, / or a system
// directory's ancestor), or an error resolving it.
func envRoot(v string) (root, why string, err error) {
	switch {
	case v == "":
		return "", "is unset", nil
	case !filepath.IsAbs(v):
		return "", "is relative", nil
	}
	r, err := filepath.EvalSymlinks(v)
	if errors.Is(err, fs.ErrNotExist) {
		return "", "does not exist", nil
	}
	if err != nil {
		return "", "", err
	}
	if d := systemAncestor(r); d != "" {
		return "", fmt.Sprintf("resolves to %s, which is or contains the system directory %s", r, d), nil
	}
	return r, "", nil
}

// systemAncestor is the system directory that path, resolved, equals or
// contains (path / contains them all), or "".
func systemAncestor(path string) string {
	for _, d := range systemDirs {
		for _, cand := range []string{d, resolvedOr(d)} {
			if path == "/" || cand == path || strings.HasPrefix(cand, path+string(filepath.Separator)) {
				return d
			}
		}
	}
	return ""
}

// resolvedOr is path with symlinks resolved, or path when it cannot be.
func resolvedOr(path string) string {
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return r
	}
	return path
}
