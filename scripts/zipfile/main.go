// zipfile builds a byte-reproducible zip: every entry's metadata is pinned
// to the -epoch timestamp instead of the filesystem, so the same inputs
// produce identical bytes on any machine, timezone, or build time. It
// replaces Compress-Archive in the Makefile release packaging, whose
// local-time DOS timestamps broke reproducibility.
package main

import (
	"archive/zip"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

func main() {
	out := flag.String("out", "", "output zip path (required)")
	epoch := flag.Int64("epoch", 0, "unix timestamp stamped on every entry (required)")
	flag.Parse()
	files := flag.Args()

	if *out == "" || *epoch == 0 || len(files) == 0 {
		fmt.Fprintln(os.Stderr, "usage: zipfile -out <zip> -epoch <unix-time> <file>...")
		os.Exit(2)
	}
	if err := writeZip(*out, *epoch, files); err != nil {
		fmt.Fprintln(os.Stderr, "zipfile:", err)
		os.Exit(1)
	}
}

// writeZip stores each file at the zip root under its base name, in argument
// order, with every header field pinned (Deflate, fixed UTC mtime, fixed
// creator/version and mode bits, no extra fields).
func writeZip(out string, epoch int64, files []string) error {
	mod := time.Unix(epoch, 0).UTC()

	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()

	w := zip.NewWriter(f)
	for _, path := range files {
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		hdr := &zip.FileHeader{
			Name:     filepath.Base(path),
			Method:   zip.Deflate,
			Modified: mod,
		}
		hdr.SetMode(0o644)
		entry, err := w.CreateHeader(hdr)
		if err != nil {
			src.Close()
			return err
		}
		if _, err := io.Copy(entry, src); err != nil {
			src.Close()
			return err
		}
		src.Close()
	}
	return w.Close()
}
