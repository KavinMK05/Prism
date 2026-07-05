//go:build darwin

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
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

