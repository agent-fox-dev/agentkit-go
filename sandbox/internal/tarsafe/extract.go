package tarsafe

import (
	"archive/tar"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
)

// Stats reports what an extraction wrote.
type Stats struct {
	// Files is the number of regular files written.
	Files int
	// Bytes is the number of bytes of file content written.
	Bytes int64
}

// Extract reads a tar stream from r and writes each entry the rules allow
// under root, creating missing parent directories. It stops at the first
// refused or malformed entry with an *EntryError; entries written before it
// stay. An error reading r itself is returned as it is.
func Extract(root *os.Root, r io.Reader) (Stats, error) {
	src := &recordingReader{r: r}
	tr := tar.NewReader(src)
	v := NewValidator(diskLookup(root))
	var st Stats
	last := ""
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return st, nil
		}
		if err != nil {
			return st, src.blame(last, err)
		}
		last = hdr.Name
		h, err := v.Check(hdr)
		if err != nil {
			return st, err
		}
		n, err := writeEntry(root, h, tr)
		if err != nil {
			if src.err != nil {
				return st, src.err
			}
			return st, &EntryError{Name: hdr.Name, Reason: err.Error()}
		}
		if h.Typeflag == tar.TypeReg {
			st.Files++
			st.Bytes += n
		}
	}
}

// writeEntry writes one checked entry through root, so no write follows a
// symlink out of it, and returns the bytes of content written.
func writeEntry(root *os.Root, hdr *tar.Header, r io.Reader) (int64, error) {
	name := path.Clean(hdr.Name)
	mode := fs.FileMode(hdr.Mode) & fs.ModePerm
	if hdr.Typeflag == tar.TypeDir {
		if name == "." {
			return 0, nil
		}
		return 0, root.MkdirAll(name, mode|0o700)
	}
	if dir := path.Dir(name); dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return 0, err
		}
	}
	// A new entry replaces an existing symlink, and a symlink replaces an
	// existing file, rather than writing through either.
	if fi, err := root.Lstat(name); err == nil && !fi.IsDir() &&
		(fi.Mode()&fs.ModeSymlink != 0 || hdr.Typeflag == tar.TypeSymlink) {
		if err := root.Remove(name); err != nil {
			return 0, err
		}
	}
	if hdr.Typeflag == tar.TypeSymlink {
		return 0, root.Symlink(hdr.Linkname, name)
	}
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return n, err
}

// diskLookup reports the symlinks already under root.
func diskLookup(root *os.Root) Lookup {
	return func(name string) (string, bool, error) {
		fi, err := root.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
			return "", false, err
		}
		target, err := root.Readlink(name)
		return target, err == nil, err
	}
}

// recordingReader remembers the error its source returned, so a failure of
// the source is not mistaken for a malformed archive.
type recordingReader struct {
	r   io.Reader
	err error
}

func (rr *recordingReader) Read(p []byte) (int, error) {
	n, err := rr.r.Read(p)
	if err != nil && err != io.EOF {
		rr.err = err
	}
	return n, err
}

// blame turns an error from tar.Reader into the source's own error, or into
// an *EntryError for a malformed archive naming the last header read.
func (rr *recordingReader) blame(last string, err error) error {
	if rr.err != nil {
		return rr.err
	}
	reason := "malformed archive: " + err.Error()
	if last != "" {
		reason = "malformed archive after this entry: " + err.Error()
	}
	return &EntryError{Name: last, Reason: reason}
}
