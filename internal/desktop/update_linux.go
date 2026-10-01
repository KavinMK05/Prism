//go:build linux

package desktop

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// runningAsAppImage reports whether the binary is running from an AppImage.
// AppImage always exports $APPIMAGE pointing at the original .AppImage file.
func runningAsAppImage() bool {
	return os.Getenv("APPIMAGE") != ""
}

func appImageAssetName() string {
	if runtime.GOARCH == "arm64" {
		return "Prism-Linux-aarch64.AppImage"
	}
	return "Prism-Linux-x86_64.AppImage"
}

func getUpdateAssetName() string {
	if runningAsAppImage() {
		return appImageAssetName()
	}
	return "Prism-Linux.tar.gz"
}

// updateAssetCandidates lists the release assets to try, in order of
// preference. Linux has a single channel per install shape: the AppImage
// replaces itself in place, a plain binary is unpacked from the tar.gz.
func updateAssetCandidates() []string {
	return []string{getUpdateAssetName()}
}

func createDestFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
}

func CleanupOldBinary() {
	if !runningAsAppImage() {
		return
	}
	// The AppImage updater swaps the running file to <path>.old; remove the
	// stale copy once the previous process has fully exited.
	oldPath := os.Getenv("APPIMAGE") + ".old"
	go func() {
		time.Sleep(5 * time.Second)
		if err := os.Remove(oldPath); err == nil {
			log.Printf("[Update] Removed old AppImage: %s", oldPath)
		}
	}()
}

// performUpdate executes the Linux update flow:
// 1. Download tar.gz to temp
// 2. Stop proxy child
// 3. Extract tar.gz over the install directory
// 4. Relaunch self and exit
func performUpdate(info *UpdateInfo, progressFn func(percent int)) error {
	if runningAsAppImage() {
		return performAppImageUpdate(info, progressFn)
	}
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get exe path: %w", err)
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return fmt.Errorf("resolve symlinks: %w", err)
	}
	installDir := filepath.Dir(exePath)

	// Step 1: Download tar.gz to temp
	tmpDir, err := os.MkdirTemp("", "prism-update")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	tarPath := filepath.Join(tmpDir, filepath.Base(info.AssetName))
	log.Printf("[Update] Downloading %s to %s", info.DownloadURL, tarPath)
	if err := downloadFile(info.DownloadURL, tarPath, progressFn); err != nil {
		return fmt.Errorf("download update: %w", err)
	}

	// Step 2: Stop the proxy child process
	StopProxyProcess()
	time.Sleep(500 * time.Millisecond)

	// Step 3: Extract tar.gz over the install directory
	log.Printf("[Update] Extracting update to %s", installDir)
	if err := extractTarGz(tarPath, installDir); err != nil {
		return fmt.Errorf("extract update: %w", err)
	}

	// Step 4: Relaunch self and exit
	log.Printf("[Update] Restarting with new version...")
	cmd := exec.Command(exePath)
	cmd.Dir = installDir
	if err := cmd.Start(); err != nil {
		log.Printf("[Update] Failed to restart: %v", err)
		return fmt.Errorf("restart: %w", err)
	}

	os.Exit(0)
	return nil
}

// performAppImageUpdate replaces the running AppImage file in place:
//  1. Download the new AppImage next to the current one (same filesystem,
//     so the swap is a plain rename)
//  2. Stop the proxy child
//  3. Swap: current -> .old, new -> current
//  4. Relaunch and exit
func performAppImageUpdate(info *UpdateInfo, progressFn func(percent int)) error {
	appImagePath := os.Getenv("APPIMAGE")
	if appImagePath == "" {
		return fmt.Errorf("APPIMAGE env var not set")
	}

	newPath := appImagePath + ".new"
	log.Printf("[Update] Downloading %s to %s", info.DownloadURL, newPath)
	if err := downloadFile(info.DownloadURL, newPath, progressFn); err != nil {
		os.Remove(newPath)
		return fmt.Errorf("download update: %w", err)
	}
	if err := os.Chmod(newPath, 0755); err != nil {
		os.Remove(newPath)
		return fmt.Errorf("chmod update: %w", err)
	}

	StopProxyProcess()
	time.Sleep(500 * time.Millisecond)

	oldPath := appImagePath + ".old"
	os.Remove(oldPath)
	if err := os.Rename(appImagePath, oldPath); err != nil {
		os.Remove(newPath)
		return fmt.Errorf("swap old: %w", err)
	}
	if err := os.Rename(newPath, appImagePath); err != nil {
		os.Rename(oldPath, appImagePath)
		return fmt.Errorf("swap new: %w", err)
	}

	log.Printf("[Update] Restarting with new version...")
	cmd := exec.Command(appImagePath)
	if err := cmd.Start(); err != nil {
		log.Printf("[Update] Failed to restart: %v", err)
		return fmt.Errorf("restart: %w", err)
	}

	os.Exit(0)
	return nil
}

func showPlatformNotification(title, message string) {
	if _, err := exec.LookPath("notify-send"); err != nil {
		log.Printf("[Update] notify-send not available, skipping notification")
		return
	}
	cmd := exec.Command("notify-send", title, message)
	if err := cmd.Start(); err != nil {
		log.Printf("[Update] Failed to show notification: %v", err)
	}
}
