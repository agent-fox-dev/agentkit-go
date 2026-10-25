package rsyncx

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// fakeStream records sent frames and replays scripted received ones, then
// end (io.EOF when nil).
type fakeStream struct {
	mu         sync.Mutex
	sent       []*sandboxpb.RsyncFrame
	recv       []*sandboxpb.RsyncFrame
	end        error
	sendErr    error
	closeSends int
}

func (f *fakeStream) Send(fr *sandboxpb.RsyncFrame) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, fr)
	return nil
}

func (f *fakeStream) Recv() (*sandboxpb.RsyncFrame, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.recv) == 0 {
		if f.end != nil {
			return nil, f.end
		}
		return nil, io.EOF
	}
	fr := f.recv[0]
	f.recv = f.recv[1:]
	return fr, nil
}

func (f *fakeStream) CloseSend() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeSends++
	return nil
}

func (f *fakeStream) Context() context.Context { return context.Background() }

// sentData is the data frames' payloads, in order.
func (f *fakeStream) sentData() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]byte
	for _, fr := range f.sent {
		out = append(out, fr.GetData())
	}
	return out
}

func dataFrame(b string) *sandboxpb.RsyncFrame {
	return &sandboxpb.RsyncFrame{Frame: &sandboxpb.RsyncFrame_Data{Data: []byte(b)}}
}

func doneFrame(files int64) *sandboxpb.RsyncFrame {
	return &sandboxpb.RsyncFrame{Frame: &sandboxpb.RsyncFrame_Done{Done: &sandboxpb.RsyncDone{Files: files}}}
}

// writeTree writes files (path -> content); a content starting with "->"
// makes a symlink to the rest.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if target, ok := strings.CutPrefix(body, "->"); ok {
			if err := os.Symlink(target, p); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// ruleTree is a tree with ignore files at two levels, .git directories,
// hidden files, and escaping and inside symlinks.
func ruleTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".gitignore": "build/\n*.log\n", "build/sub/deep.txt": "d", "x.log": "x", "keep.txt": "k",
		".git/HEAD": "ref", ".git/info/exclude": "secret.txt\n", "secret.txt": "s",
		"nested/.git/HEAD": "ref", "nested/.gitignore": "tmp/\n", "nested/tmp/a": "a", "nested/n.txt": "n",
		".hidden": "h", "esc": "->/etc", "up": "->../outside", "in": "->keep.txt", "sub/in2": "->../keep.txt",
	})
	return root
}

func openRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

// walkOracle is every entry filepath.WalkDir finds that workspace.Walk with
// ig does not visit, whose parent Walk visits (or is the root), and that has
// no .git component.
func walkOracle(t *testing.T, root string, ig workspace.IgnoreOptions) []string {
	t.Helper()
	root, _ = filepath.EvalSymlinks(root)
	ws, err := workspace.Open(root, workspace.Options{Ignore: ig})
	if err != nil {
		t.Fatal(err)
	}
	visited := map[string]bool{".": true}
	if err := ws.Walk(context.Background(), "", workspace.WalkOptions{IncludeHidden: true},
		func(rel string, _ fs.DirEntry) error { visited[rel] = true; return nil }); err != nil {
		t.Fatal(err)
	}
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if slices.Contains(strings.Split(rel, "/"), ".git") {
			return skip(d)
		}
		if !visited[rel] && visited[path.Dir(rel)] {
			out = append(out, rel)
			return skip(d)
		}
		return nil
	})
	return out
}

// end is one side of a kernel-buffered duplex pipe; net.Pipe's unbuffered
// writes would deadlock two peers writing at once.
type end struct {
	io.Reader
	io.WriteCloser
	r io.Closer
}

func (e *end) Close() error { _ = e.r.Close(); return e.WriteCloser.Close() }

func osPipe(t *testing.T) (*end, *end) {
	t.Helper()
	ar, bw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	br, aw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	a, b := &end{Reader: ar, WriteCloser: aw, r: ar}, &end{Reader: br, WriteCloser: bw, r: br}
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b
}

// listTree is every entry under root, relative and sorted.
func listTree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
		if err == nil && p != root {
			rel, _ := filepath.Rel(root, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	slices.Sort(out)
	return out
}
