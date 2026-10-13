package tarsafe

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
)

// genEntry is one generated tar entry and whether the entry rules allow it.
type genEntry struct {
	hdr     tar.Header
	body    string
	allowed bool
}

// genStream builds a random entry sequence. Allowed entries only refer to
// what earlier allowed entries created, so the oracle is the allowed flag.
func genStream(r *rand.Rand) []genEntry {
	modes := []int64{0o644, 0o755, 0o4755, 0o2755, 0o1777, 0o6777, 0o600}
	var dirs, links []string
	var out []genEntry
	n := 1 + r.IntN(10)
	for i := range n {
		mode := modes[r.IntN(len(modes))]
		name := fmt.Sprintf("e%d", i)
		var e genEntry
		switch k := r.IntN(14); {
		case k < 3: // a regular file, maybe in an earlier directory
			if len(dirs) > 0 && r.IntN(2) == 0 {
				name = dirs[r.IntN(len(dirs))] + "/" + name
			}
			e = genEntry{tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: mode}, "body " + name, true}
		case k < 5:
			e = genEntry{hdr: tar.Header{Typeflag: tar.TypeDir, Name: name + "/", Mode: mode | 0o700}, allowed: true}
			dirs = append(dirs, name)
		case k == 5 && len(dirs) > 0: // a symlink to an earlier directory
			e = genEntry{hdr: tar.Header{Typeflag: tar.TypeSymlink, Name: name, Linkname: dirs[r.IntN(len(dirs))], Mode: 0o777}, allowed: true}
			links = append(links, name)
		case k == 6 && len(links) > 0: // a file written through an inside symlink
			e = genEntry{tar.Header{Typeflag: tar.TypeReg, Name: links[r.IntN(len(links))] + "/" + name, Mode: mode}, "through", true}
		case k == 7:
			target := []string{"../outside", "/etc", "a/../../x", "../" + name}[r.IntN(4)]
			e = genEntry{hdr: tar.Header{Typeflag: tar.TypeSymlink, Name: name, Linkname: target, Mode: 0o777}}
		case k == 8:
			e = genEntry{hdr: tar.Header{Typeflag: tar.TypeLink, Name: name, Linkname: "e0"}}
		case k == 9:
			flag := []byte{tar.TypeChar, tar.TypeBlock, tar.TypeFifo, 'Z'}[r.IntN(4)]
			e = genEntry{hdr: tar.Header{Typeflag: flag, Name: name, Mode: 0o644}}
		case k == 10:
			e = genEntry{tar.Header{Typeflag: tar.TypeReg, Name: "/abs/" + name, Mode: mode}, "x", false}
		case k == 11:
			e = genEntry{tar.Header{Typeflag: tar.TypeReg, Name: []string{"../", "a/../", "a/b/../../../"}[r.IntN(3)] + name, Mode: mode}, "x", false}
		default:
			e = genEntry{tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: mode}, "plain", true}
		}
		e.hdr.Size = int64(len(e.body))
		out = append(out, e)
	}
	return out
}

func buildTar(t *testing.T, es []genEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range es {
		h := e.hdr
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatalf("writing %q: %v", h.Name, err)
		}
		if _, err := io.WriteString(tw, e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const specialModes = os.ModeSetuid | os.ModeSetgid | os.ModeSticky

// TS-13-48: the entry rules accept exactly the streams made of allowed
// entries, refuse at the first forbidden entry, and clear special mode bits.
// 13-REQ-8.2.
func TestEntryRulesProperty_TS13_48(t *testing.T) {
	r := rand.New(rand.NewPCG(48, 48))
	for i := range 500 {
		es := genStream(r)
		blob := buildTar(t, es)
		base := t.TempDir()
		target, outside := filepath.Join(base, "t"), filepath.Join(base, "outside")
		for _, d := range []string{target, outside} {
			if err := os.Mkdir(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		root, err := os.OpenRoot(target)
		if err != nil {
			t.Fatal(err)
		}
		_, e1 := Extract(root, bytes.NewReader(blob))
		root.Close()
		var validated bytes.Buffer
		e2 := ValidateStream(bytes.NewReader(blob), &validated)

		firstBad := -1
		for j, e := range es {
			if !e.allowed {
				firstBad = j
				break
			}
		}
		if (e1 == nil) != (firstBad < 0) || (e2 == nil) != (firstBad < 0) {
			t.Fatalf("stream %d %+v: extract %v, validate %v, oracle first refused %d", i, es, e1, e2, firstBad)
		}
		if firstBad >= 0 {
			want := es[firstBad].hdr.Name
			for _, err := range []error{e1, e2} {
				var ee *EntryError
				if !errors.As(err, &ee) || ee.Name != want {
					t.Fatalf("stream %d: %v does not name the first refused entry %q", i, err, want)
				}
			}
		}

		err = filepath.WalkDir(target, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			fi, err := d.Info()
			if err != nil {
				return err
			}
			if fi.Mode()&specialModes != 0 {
				t.Errorf("stream %d: %s has mode %v", i, p, fi.Mode())
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		tr := tar.NewReader(&validated)
		for {
			h, err := tr.Next()
			if err != nil {
				break
			}
			if h.Mode&0o7000 != 0 {
				t.Errorf("stream %d: validated header %q keeps mode %o", i, h.Name, h.Mode)
			}
		}
		if ents, _ := os.ReadDir(outside); len(ents) != 0 {
			t.Fatalf("stream %d wrote outside the target: %v", i, ents)
		}
	}
}
