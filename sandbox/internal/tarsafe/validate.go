package tarsafe

import (
	"archive/tar"
	"io"
)

// ValidateStream reads an untrusted tar stream from r, applies the entry
// rules to each entry, and writes each accepted entry, sanitised, to w. On
// the first refused or malformed entry it flushes what was written and
// returns an *EntryError without writing that entry or anything after it.
// An error writing to w is returned as it is.
func ValidateStream(r io.Reader, w io.Writer) error {
	src := &recordingReader{r: r}
	tr := tar.NewReader(src)
	dst := &recordingWriter{w: w}
	tw := tar.NewWriter(dst)
	v := NewValidator(nil)
	last := ""
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return tw.Close()
		}
		if err != nil {
			_ = tw.Flush()
			return src.blame(last, err)
		}
		last = hdr.Name
		h, err := v.Check(hdr)
		if err != nil {
			_ = tw.Flush()
			return err
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := io.Copy(tw, tr); err != nil {
				if dst.err != nil {
					return dst.err
				}
				return src.blame(last, err)
			}
		}
	}
}

// recordingWriter remembers the error its destination returned, so a
// failing destination is not mistaken for a malformed archive.
type recordingWriter struct {
	w   io.Writer
	err error
}

func (rw *recordingWriter) Write(p []byte) (int, error) {
	n, err := rw.w.Write(p)
	if err != nil {
		rw.err = err
	}
	return n, err
}
