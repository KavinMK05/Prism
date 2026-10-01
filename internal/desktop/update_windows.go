//go:build windows

package desktop

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"ollama-proxy/internal/platform"
)

// Release asset names. The MSI name is produced by the release workflow and is
// looked up by exact match, so it has to keep the name
// installer/windows/build-msi.ps1 writes.
const (
	portableUpdateAssetName = "prism.exe"
	msiUpdateAssetName      = "Prism-Windows-x64.msi"
)

func getUpdateAssetName() string {
	return portableUpdateAssetName
}

// updateAssetCandidates lists the release assets to try, in order of
// preference for how this copy of Prism is installed. An MSI install takes the
// package (and only the package: falling back to the portable exe would
// overwrite a file Windows Installer owns), everything else takes the
// single-file build that replaces itself.
func updateAssetCandidates() []string {
	if InstalledViaMSI() {
		return []string{msiUpdateAssetName}
	}
	return []string{getUpdateAssetName()}
}

func createDestFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
}

func CleanupOldBinary() {
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

// performUpdate installs an available update, choosing the flow that matches
// how this copy of Prism was installed.
func performUpdate(info *UpdateInfo, progressFn func(percent int)) error {
	if isMSIAsset(info.AssetName) {
		return performMSIUpdate(info, progressFn)
	}
	return performPortableUpdate(info, progressFn)
}

// performPortableUpdate executes the update flow for a self-contained
// prism.exe:
// 1. Rename running exe to .old
// 2. Download new exe
// 3. Stop proxy child
// 4. Relaunch self and exit
func performPortableUpdate(info *UpdateInfo, progressFn func(percent int)) error {
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
	StopProxyProcess()

	// Step 4: Relaunch self and exit
	log.Printf("[Update] Restarting with new version...")
	cmd := exec.Command(exePath)
	cmd.Dir = filepath.Dir(exePath)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: CREATE_NO_WINDOW}
	if err := cmd.Start(); err != nil {
		// Try to rollback
		log.Printf("[Update] Failed to restart: %v", err)
		return fmt.Errorf("restart: %w", err)
	}

	os.Exit(0)
	return nil
}

// performMSIUpdate installs a downloaded MSI through Windows Installer.
//
// The portable flow cannot be used here: prism.exe sits in a directory the
// installer owns, and the file is versioned in the installed-product database.
// Writing over it would be undone by the next repair or upgrade. Handing the
// update to msiexec keeps that database consistent, and the package's
// CloseApplication action terminates whatever is still running.
func performMSIUpdate(info *UpdateInfo, progressFn func(percent int)) error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get exe path: %w", err)
	}

	msiDir := filepath.Join(os.TempDir(), "prism-update")
	// Clear the previous update's download first. msiexec caches its own copy of
	// the package, so nothing here is needed once an install has finished, and
	// this keeps the directory to a single file. A failure here is not fatal:
	// the download below overwrites the file it needs.
	if err := os.RemoveAll(msiDir); err != nil {
		log.Printf("[Update] Could not clear %s: %v", msiDir, err)
	}
	if err := os.MkdirAll(msiDir, 0755); err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	msiPath := filepath.Join(msiDir, filepath.Base(info.AssetName))

	// Step 1: Download the package
	log.Printf("[Update] Downloading %s to %s", info.DownloadURL, msiPath)
	if err := downloadFile(info.DownloadURL, msiPath, progressFn); err != nil {
		os.Remove(msiPath)
		return fmt.Errorf("download update: %w", err)
	}

	// Step 2: Stop the proxy child. The tray process itself has to exit for
	// msiexec to replace prism.exe; the detached helper below waits for the
	// install and then restarts the app.
	StopProxyProcess()

	if err := startMSIUpdateHelper(msiPath, exePath); err != nil {
		// Nothing has been installed, so leave the user with a working app.
		StartProxyProcess()
		return fmt.Errorf("start installer: %w", err)
	}

	log.Printf("[Update] Handed %s to msiexec; restarting after the install", filepath.Base(msiPath))
	os.Exit(0)
	return nil
}

// startMSIUpdateHelper launches a detached helper that runs msiexec, waits for
// it to finish and then starts Prism again. The wait lives in a separate
// process because msiexec returns as soon as it hands the install to the
// Windows Installer service, and because this process is about to exit.
func startMSIUpdateHelper(msiPath, exePath string) error {
	logDir := platform.LogDir()
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}
	logPath := filepath.Join(logDir, "update.log")

	script := fmt.Sprintf(`$msi = '%s'
$exe = '%s'
$log = '%s'
try {
    $p = Start-Process -FilePath 'msiexec.exe' -ArgumentList @('/i', ('"' + $msi + '"'), '/qb', '/norestart') -PassThru -Wait
    if ($p.ExitCode -ne 0) {
        Add-Content -Path $log -Value ((Get-Date -Format s) + ' msiexec exited with ' + $p.ExitCode + ' for ' + $msi)
    }
} catch {
    Add-Content -Path $log -Value ((Get-Date -Format s) + ' msiexec failed: ' + $_)
}
Start-Process -FilePath $exe
`, escapePS(msiPath), escapePS(exePath), escapePS(logPath))

	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", script)
	cmd.Dir = filepath.Dir(exePath)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: CREATE_NO_WINDOW}
	return cmd.Start()
}

// isMSIAsset reports whether a release asset is a Windows Installer package.
func isMSIAsset(name string) bool {
	return strings.EqualFold(filepath.Ext(name), ".msi")
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
