//go:build darwin

package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func getUpdateAssetName() string {
	return "Prism-macOS.tar.gz"
}

func createDestFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
}

func cleanupOldBinary() {
	// No .old cleanup needed on macOS since we replace the whole .app bundle
}

// getAppBundlePath returns the path to the Prism.app bundle from the executable path.
// The executable is at Prism.app/Contents/MacOS/prism, so walk up 3 levels.
func getAppBundlePath() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("get exe path: %w", err)
	}
	// Resolve symlinks
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return "", fmt.Errorf("resolve symlinks: %w", err)
	}
	// exePath = .../Prism.app/Contents/MacOS/prism
	// bundle = .../Prism.app
	bundle := filepath.Dir(filepath.Dir(filepath.Dir(exePath)))
	if filepath.Base(bundle) != "Prism.app" {
		// Check without EvalSymlinks too
		exePath2, _ := os.Executable()
		bundle2 := filepath.Dir(filepath.Dir(filepath.Dir(exePath2)))
		if filepath.Base(bundle2) == "Prism.app" {
			return bundle2, nil
		}
		return "", fmt.Errorf("could not locate Prism.app bundle (got %s)", bundle)
	}
	return bundle, nil
}

// performUpdate executes the macOS update flow:
// 1. Download tar.gz to temp
// 2. Stop proxy child
// 3. Extract tar.gz over the app bundle parent dir
// 4. Relaunch self and exit
func performUpdate(info *UpdateInfo, progressFn func(percent int)) error {
	// Determine where Prism.app lives
	bundlePath, err := getAppBundlePath()
	if err != nil {
		return fmt.Errorf("locate app bundle: %w", err)
	}
	parentDir := filepath.Dir(bundlePath)

	// Step 1: Download tar.gz to temp
	tmpDir, err := os.MkdirTemp("", "prism-update")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	tarPath := filepath.Join(tmpDir, "Prism-macOS.tar.gz")
	log.Printf("[Update] Downloading %s to %s", info.DownloadURL, tarPath)
	if err := downloadFile(info.DownloadURL, tarPath, progressFn); err != nil {
		return fmt.Errorf("download update: %w", err)
	}

	// Step 2: Stop the proxy child process
	stopProxyProcess()
	time.Sleep(500 * time.Millisecond)

	// Step 3: Extract tar.gz over the app bundle
	log.Printf("[Update] Extracting update to %s", parentDir)
	if err := extractTarGz(tarPath, parentDir); err != nil {
		return fmt.Errorf("extract update: %w", err)
	}

	// Step 4: Relaunch self and exit
	log.Printf("[Update] Restarting with new version...")
	cmd := exec.Command("open", bundlePath)
	if err := cmd.Start(); err != nil {
		log.Printf("[Update] Failed to restart: %v", err)
		return fmt.Errorf("restart: %w", err)
	}

	os.Exit(0)
	return nil
}

// extractTarGz extracts a .tar.gz file to the destination directory.
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
			log.Printf("[Update] Skipping unsafe path: %s", hdr.Name)
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
				log.Printf("[Update] Skipping unsafe symlink: %s -> %s", hdr.Name, hdr.Linkname)
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

func showPlatformNotification(title, message string) {
	script := fmt.Sprintf(`display notification "%s" with title "%s"`,
		escapeAppleScript(message),
		escapeAppleScript(title),
	)
	cmd := exec.Command("osascript", "-e", script)
	if err := cmd.Start(); err != nil {
		log.Printf("[Update] Failed to show notification: %v", err)
	}
}

// isPathSafe checks that target is within base (prevents path traversal).
func isPathSafe(base, target string) bool {
	absBase, _ := filepath.Abs(base)
	absTarget, _ := filepath.Abs(target)
	return strings.HasPrefix(absTarget, absBase+string(filepath.Separator)) || absTarget == absBase
}
