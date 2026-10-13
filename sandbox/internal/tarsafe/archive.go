package tarsafe

import (
	"archive/tar"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
)

// Archive writes the subtree rel of root to w as a tar stream, verbatim:
// hidden and ignored files too, symlinks stored as symlinks and never
// followed, entry types other than regular files, directories and symlinks
// skipped, and names relative to rel. A rel naming a single file archives
// that file under its base name.
func Archive(root *os.Root, rel string, w io.Writer) error {
	fi, err := root.Lstat(rel)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(w)
	if !fi.IsDir() {
		if err := addEntry(root, tw, path.Base(rel), rel, fi); err != nil {
			return err
		}
		return tw.Close()
	}
	err = fs.WalkDir(root.FS(), rel, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == rel {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		name := p
		if rel != "." {
			name = strings.TrimPrefix(p, rel+"/")
		}
		return addEntry(root, tw, name, p, info)
	})
	if err != nil {
		return err
	}
	return tw.Close()
}

// addEntry writes the entry at p (relative to root) to tw under name.
func addEntry(root *os.Root, tw *tar.Writer, name, p string, fi fs.FileInfo) error {
	link := ""
	switch {
	case fi.Mode().IsRegular(), fi.IsDir():
	case fi.Mode()&fs.ModeSymlink != 0:
		target, err := root.Readlink(p)
		if err != nil {
			return err
		}
		link = target
	default:
		return nil
	}
	hdr, err := tar.FileInfoHeader(fi, link)
	if err != nil {
		return err
	}
	hdr.Name = name
	if fi.IsDir() {
		hdr.Name += "/"
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return nil
	}
	f, err := root.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(tw, f)
	return err
}
