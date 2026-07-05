package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// extractTarGz extracts a .tar.gz file to the destination directory.
// Cross-platform (stdlib only); used by the macOS self-update flow and the
// SearXNG python-build-standalone install on both Windows and macOS.
func extractTarGz(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open tar.gz: %w", err)
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar next: %w", err)
		}

		// Sanitize path to prevent path traversal
		target := filepath.Join(dst, hdr.Name)
		if !isPathSafe(dst, target) {
			log.Printf("[Archive] Skipping unsafe path: %s", hdr.Name)
			continue
		}

		// On Windows, skip entries whose final path component contains characters
		// that NTFS cannot represent (e.g. the SearXNG repo ships config templates
		// named "searxng.conf:socket"). These are never needed at runtime.
		if runtime.GOOS == "windows" && hasWindowsIllegalChar(filepath.Base(hdr.Name)) {
			log.Printf("[Archive] Skipping Windows-illegal path: %s", hdr.Name)
			continue
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)); err != nil {
				return fmt.Errorf("mkdir %s: %w", target, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return fmt.Errorf("mkdir parent %s: %w", filepath.Dir(target), err)
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return fmt.Errorf("create %s: %w", target, err)
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return fmt.Errorf("write %s: %w", target, err)
			}
			out.Close()
		case tar.TypeSymlink:
			if !isPathSafe(dst, filepath.Join(dst, hdr.Linkname)) {
				log.Printf("[Archive] Skipping unsafe symlink: %s -> %s", hdr.Name, hdr.Linkname)
				continue
			}
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return fmt.Errorf("mkdir parent %s: %w", filepath.Dir(target), err)
			}
			os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return fmt.Errorf("symlink %s -> %s: %w", target, hdr.Linkname, err)
			}
		}
	}

	return nil
}

// isPathSafe checks that target is within base (prevents path traversal).
func isPathSafe(base, target string) bool {
	absBase, _ := filepath.Abs(base)
	absTarget, _ := filepath.Abs(target)
	return strings.HasPrefix(absTarget, absBase+string(filepath.Separator)) || absTarget == absBase
}

// hasWindowsIllegalChar reports whether name contains a character that NTFS
// cannot represent in a filename. Used to skip such tar entries on Windows
// instead of failing the whole extraction.
func hasWindowsIllegalChar(name string) bool {
	for _, c := range name {
		switch c {
		case '<', '>', ':', '"', '/', '\\', '|', '?', '*':
			return true
		}
	}
	return false
}