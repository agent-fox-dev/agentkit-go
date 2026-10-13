package tarsafe

import (
	"archive/tar"
	"io"
)

// ValidateStream reads an untrusted tar stream from r, applies the entry
// rules to each entry, and writes each accepted entry, sanitised, to w. On
// the first refused or malformed entry it flushes what was written and
// returns an *EntryError without writing that entry or anything after it.
func ValidateStream(r io.Reader, w io.Writer) error {
	src := &recordingReader{r: r}
	tr := tar.NewReader(src)
	tw := tar.NewWriter(w)
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
				return src.blame(last, err)
			}
		}
	}
}
