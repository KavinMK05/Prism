//go:build windows

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

func getUpdateAssetName() string {
	return "prism.exe"
}

func createDestFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
}

func cleanupOldBinary() {
	go func() {
		time.Sleep(5 * time.Second)
		exePath, err := os.Executable()
		if err != nil {
			return
		}
		oldPath := exePath + ".old"
		if _, err := os.Stat(oldPath); err == nil {
			if err := os.Remove(oldPath); err != nil {
				log.Printf("[Update] Failed to remove old binary: %v", err)
			} else {
				log.Printf("[Update] Removed old binary: %s", oldPath)
			}
		}
	}()
}

// performUpdate executes the Windows update flow:
// 1. Rename running exe to .old
// 2. Download new exe
// 3. Stop proxy child
// 4. Relaunch self and exit
func performUpdate(info *UpdateInfo, progressFn func(percent int)) error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get exe path: %w", err)
	}

	oldPath := exePath + ".old"

	// Step 1: Rename current exe to .old
	log.Printf("[Update] Renaming %s -> %s", exePath, oldPath)
	if err := os.Rename(exePath, oldPath); err != nil {
		return fmt.Errorf("rename current exe: %w", err)
	}

	// Step 2: Download new exe
	log.Printf("[Update] Downloading %s to %s", info.DownloadURL, exePath)
	if err := downloadFile(info.DownloadURL, exePath, progressFn); err != nil {
		// Rollback: rename .old back
		log.Printf("[Update] Download failed, rolling back: %v", err)
		if rollbackErr := os.Rename(oldPath, exePath); rollbackErr != nil {
			log.Printf("[Update] CRITICAL: Rollback failed! %s -> %s: %v", oldPath, exePath, rollbackErr)
		}
		return fmt.Errorf("download update: %w", err)
	}

	// Step 3: Stop the proxy child process
	stopProxyProcess()

	// Step 4: Relaunch self and exit
	log.Printf("[Update] Restarting with new version...")
	cmd := exec.Command(exePath)
	cmd.Dir = filepath.Dir(exePath)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
	if err := cmd.Start(); err != nil {
		// Try to rollback
		log.Printf("[Update] Failed to restart: %v", err)
		return fmt.Errorf("restart: %w", err)
	}

	os.Exit(0)
	return nil
}

func showPlatformNotification(title, message string) {
	script := fmt.Sprintf(
		`[void] [System.Reflection.Assembly]::LoadWithPartialName('System.Windows.Forms'); $n = New-Object System.Windows.Forms.NotifyIcon; $n.Icon = [System.Drawing.SystemIcons]::Information; $n.Visible = $true; $n.ShowBalloonTip(5000, '%s', '%s', [System.Windows.Forms.ToolTipIcon]::Info); Start-Sleep -Seconds 6; $n.Dispose()`,
		escapePS(title),
		escapePS(message),
	)
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
	if err := cmd.Start(); err != nil {
		log.Printf("[Update] Failed to show notification: %v", err)
	}
}

func escapePS(s string) string {
	result := ""
	for _, ch := range s {
		if ch == '\'' {
			result += "''"
		} else if ch >= 32 && ch != '<' && ch != '>' && ch != '&' && ch != '|' {
			result += string(ch)
		}
	}
	return result
}
