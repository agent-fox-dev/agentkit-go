//go:build !windows

package codesearch

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agent-fox-dev/agentkit-go/outline"
)

// storeFormatVersion is the version of the stored index's layout. A process
// treats a stored index of another version as absent and builds anew.
const storeFormatVersion = 1

// storeManifest is manifest.json of a workspace's store directory: which
// stored index is current, for which root, in which format. It is replaced by
// a rename, so a reader sees the previous manifest or the next one.
type storeManifest struct {
	FormatVersion int    `json:"format_version"`
	Root          string `json:"root"`
	// Index is the directory, under the store directory, that holds the
	// shards and state.json.
	Index         string `json:"index"`
	Partial       bool   `json:"partial"`
	PartialReason string `json:"partial_reason,omitempty"`
}

// storedFile is a file's record as revalidation needs it, with the times in
// Unix nanoseconds so they compare equal after a round trip.
type storedFile struct {
	Size      int64  `json:"size"`
	MTime     int64  `json:"mtime"`
	IndexedAt int64  `json:"indexed_at"`
	Hash      uint64 `json:"hash"`
}

// storedState is state.json of a stored index: what the index keeps in memory
// beside its shards.
type storedState struct {
	Files    map[string]storedFile   `json:"files"`
	Outlines map[string]outline.File `json:"outlines"`
	Indexed  []string                `json:"indexed"`
	Stats    BuildStatsResult        `json:"stats"`
}

// outsideWorkspace is dir made absolute, or an error when it is the
// workspace root or lies inside it, after resolving the symbolic links of
// the parts of it that exist. It touches no disk beyond reading links.
func outsideWorkspace(root, dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("codesearch: index directory %s: %w", dir, err)
	}
	rel, err := filepath.Rel(root, resolveExisting(abs))
	if err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))) {
		return "", fmt.Errorf("codesearch: index directory %s must be outside the workspace %s", dir, root)
	}
	return abs, nil
}

// resolveExisting is abs with the symbolic links of its longest existing
// prefix resolved, and the rest appended as it is.
func resolveExisting(abs string) string {
	rest := ""
	for p := abs; ; p = filepath.Dir(p) {
		if _, err := os.Lstat(p); err == nil {
			if r, err := filepath.EvalSymlinks(p); err == nil {
				return filepath.Join(r, rest)
			}
			return abs
		}
		if filepath.Dir(p) == p {
			return abs
		}
		rest = filepath.Join(filepath.Base(p), rest)
	}
}

// storeDirPath is the workspace's store directory under IndexDir.
func (idx *Index) storeDirPath() string {
	return filepath.Join(idx.opts.IndexDir, idx.hashDir())
}

// store keeps the build in runDir under IndexDir, when it is set: the shards
// and state.json are written under a temporary name, renamed into place, and
// only then named by a new manifest, so no reader follows a manifest to a
// half-written index. A store that fails leaves the process its run
// directory, as without IndexDir, and stores nothing.
func (idx *Index) store(runDir string, out *buildOutput) {
	if idx.opts.IndexDir == "" {
		return
	}
	_ = idx.writeStore(runDir, out)
}

func (idx *Index) writeStore(runDir string, out *buildOutput) error {
	storeDir := idx.storeDirPath()
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		return err
	}
	name := "index-" + randomID()
	tmp := filepath.Join(storeDir, name+".tmp")
	if err := os.Mkdir(tmp, 0o700); err != nil {
		return err
	}
	fail := func(err error) error {
		_ = os.RemoveAll(tmp)
		return err
	}
	if err := copyFiles(runDir, tmp); err != nil {
		return fail(err)
	}
	if err := writeJSON(filepath.Join(tmp, "state.json"), stateOf(out)); err != nil {
		return fail(err)
	}
	if idx.testStoreHook != nil {
		idx.testStoreHook()
	}
	final := filepath.Join(storeDir, name)
	if err := os.Rename(tmp, final); err != nil {
		return fail(err)
	}
	m := storeManifest{FormatVersion: storeFormatVersion, Root: idx.ws.Root(), Index: name,
		Partial: out.partial, PartialReason: out.partialReason}
	if err := writeJSON(filepath.Join(storeDir, "manifest.json"), m); err != nil {
		_ = os.RemoveAll(final)
		return err
	}
	// The indexes the manifest no longer names go; a reader works on its own
	// copy of the shards, so none is read from here.
	entries, _ := os.ReadDir(storeDir)
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "index-") && e.Name() != name && !strings.HasSuffix(e.Name(), ".tmp") {
			_ = os.RemoveAll(filepath.Join(storeDir, e.Name()))
		}
	}
	return nil
}

// stateOf is what state.json keeps of a build.
func stateOf(out *buildOutput) storedState {
	st := storedState{Files: make(map[string]storedFile, len(out.fileInfos)), Outlines: out.outlineFiles, Stats: out.stats}
	for rel, fi := range out.fileInfos {
		st.Files[rel] = storedFile{Size: fi.size, MTime: fi.mtime.UnixNano(), IndexedAt: fi.indexedAt.UnixNano(), Hash: fi.hash}
	}
	for rel := range out.indexedFiles {
		st.Indexed = append(st.Indexed, rel)
	}
	return st
}

// writeJSON writes v to path through a temporary file and a rename, so a
// reader sees the old file or the new one.
func writeJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + "." + randomID() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// copyFiles copies the regular files of src into dst.
func copyFiles(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		if err := copyFile(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// randomID is 16 random hex characters, or a time-based name when the
// system's randomness fails.
func randomID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
