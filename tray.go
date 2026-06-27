package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/getlantern/systray"
)

var (
	cfg            *Config
	statusItem     *systray.MenuItem
	startItem      *systray.MenuItem
	stopItem       *systray.MenuItem
	mRestart       *systray.MenuItem
	versionItem    *systray.MenuItem
	checkUpdateItem *systray.MenuItem
	updateAvailItem *systray.MenuItem
	logFile        *os.File
	logFileMu      sync.Mutex
	proxyPID       int
	proxyCmd       *exec.Cmd
	proxyRunningMu sync.Mutex
	updateState    UpdateState
	updateInfo     *UpdateInfo
	updateMu       sync.Mutex
)

func getAdminPort() string {
	port := os.Getenv("PRISM_ADMIN_PORT")
	if port == "" {
		port = "8765"
	}
	return port
}

func runTray(iconData []byte, cleanup func()) {
	cfg = loadConfig()

	// Start the admin UI server in the tray process
	startAdminServer(cfg, getAdminPort())

	systray.Run(func() {
		setPlatformIcon(iconData)
		systray.SetTooltip("Prism")

		running := isProxyRunning()

		versionItem = systray.AddMenuItem("Prism "+version, "Current version")
		versionItem.Disable()

		systray.AddSeparator()

		statusItem = systray.AddMenuItem("● Checking...", "Proxy status")
		statusItem.Disable()

		systray.AddSeparator()

		startItem = systray.AddMenuItem("Start Proxy", "Start Prism proxy")
		stopItem = systray.AddMenuItem("Stop Proxy", "Stop Prism proxy")
		mRestart = systray.AddMenuItem("Restart Proxy", "Restart Prism proxy")

		systray.AddSeparator()

		openSettingsItem := systray.AddMenuItem("Open Settings", "Open web-based settings panel")
		openFolderItem := systray.AddMenuItem("Open Folder", "Open proxy directory")
		editModelConfigItem := systray.AddMenuItem("Edit Model Config", "Open model remapping config in editor")
		showLogsItem := systray.AddMenuItem("Show Logs", "Open a console window with live logs")

		systray.AddSeparator()

		checkUpdateItem = systray.AddMenuItem("Check for Updates", "Check for newer versions")
		updateAvailItem = systray.AddMenuItem("", "Install update")
		updateAvailItem.Hide()

		systray.AddSeparator()

		quitItem := systray.AddMenuItem("Quit", "Quit tray (stops proxy too)")

		updateMenu(running)

		if !running {
			startProxyProcess()
			time.Sleep(500 * time.Millisecond)
			updateMenu(isProxyRunning())
		}

		// Start background usage refresh for OAuth accounts
		startUsageRefreshLoop()

		// Start background update checker
		startUpdateCheckLoop()

		go func() {
			for {
				select {
				case <-startItem.ClickedCh:
					startProxyProcess()
					time.Sleep(500 * time.Millisecond)
					updateMenu(isProxyRunning())
				case <-stopItem.ClickedCh:
					stopProxyProcess()
					time.Sleep(500 * time.Millisecond)
					updateMenu(isProxyRunning())
				case <-mRestart.ClickedCh:
					stopProxyProcess()
					time.Sleep(500 * time.Millisecond)
					startProxyProcess()
					time.Sleep(500 * time.Millisecond)
					updateMenu(isProxyRunning())
				case <-openSettingsItem.ClickedCh:
					openAdminUI(getAdminPort())
				case <-openFolderItem.ClickedCh:
					openInFileExplorer(filepath.Dir(getExePath()))
				case <-editModelConfigItem.ClickedCh:
					editModelConfig()
				case <-showLogsItem.ClickedCh:
					openLogsConsole()
				case <-checkUpdateItem.ClickedCh:
					go manualUpdateCheck()
				case <-updateAvailItem.ClickedCh:
					go installUpdate()
				case <-quitItem.ClickedCh:
					stopProxyProcess()
					closeLogFile()
					if cleanup != nil {
						cleanup()
					}
					systray.Quit()
				}
			}
		}()
	}, func() {})
}

func updateMenu(running bool) {
	if running {
		statusItem.SetTitle("● Running")
		startItem.Disable()
		stopItem.Enable()
		mRestart.Enable()
	} else {
		statusItem.SetTitle("○ Stopped")
		startItem.Enable()
		stopItem.Disable()
		mRestart.Disable()
	}
}

func startProxyProcess() {
	if isProxyRunning() {
		return
	}

	killOrphanOnPort()

	logFileMu.Lock()
	closeLogFileLocked()

	logDir := getLogDir()
	os.MkdirAll(logDir, 0755)
	logPath := filepath.Join(logDir, "proxy.log")
	var err error
	logFile, err = os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		logFileMu.Unlock()
		log.Printf("Failed to create log file: %v", err)
		return
	}
	logFileMu.Unlock()

	exe := getExePath()
	cmd := runHidden(exec.Command(exe, "--serve"))
	env := os.Environ()
	filtered := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, "OLLAMA_API_KEY=") && !strings.HasPrefix(e, "OPENCODE_GO_API_KEY=") {
			filtered = append(filtered, e)
		}
	}
	cmd.Env = append(filtered, "OLLAMA_API_KEY="+cfg.getDefaultAPIKey())
	cmd.Dir = filepath.Dir(exe)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		log.Printf("Failed to start proxy: %v", err)
		return
	}

	proxyRunningMu.Lock()
	proxyPID = cmd.Process.Pid
	proxyCmd = cmd
	proxyRunningMu.Unlock()

	go func() {
		err := cmd.Wait()
		proxyRunningMu.Lock()
		proxyPID = 0
		proxyCmd = nil
		proxyRunningMu.Unlock()
		if err != nil {
			log.Printf("Proxy process exited with error: %v", err)
		}
	}()
}

func closeLogFileMutex() {
	logFileMu.Lock()
	defer logFileMu.Unlock()
	if logFile != nil {
		logFile.Close()
		logFile = nil
	}
}

func closeLogFile() {
	logFileMu.Lock()
	defer logFileMu.Unlock()
	closeLogFileLocked()
}

func closeLogFileLocked() {
	if logFile != nil {
		logFile.Close()
		logFile = nil
	}
}

func getLogFilePath() string {
	return filepath.Join(getLogDir(), "proxy.log")
}

func startUpdateCheckLoop() {
	go func() {
		// Check on startup (after a brief delay)
		time.Sleep(10 * time.Second)
		doUpdateCheck()

		// Then check every 24 hours
		ticker := time.NewTicker(updateCheckInterval)
		defer ticker.Stop()
		for range ticker.C {
			doUpdateCheck()
		}
	}()
}

func doUpdateCheck() {
	updateMu.Lock()
	if updateState == UpdateDownloading || updateState == UpdateReady || updateState == UpdateChecking {
		updateMu.Unlock()
		return
	}
	updateState = UpdateChecking
	updateMu.Unlock()

	checkUpdateItem.SetTitle("Checking for updates...")
	checkUpdateItem.Disable()

	info, err := checkForUpdate()

	updateMu.Lock()
	defer updateMu.Unlock()

	if err != nil {
		updateState = UpdateIdle
		updateInfo = nil
		updateAvailItem.Hide()
		checkUpdateItem.SetTitle("Check for Updates")
		checkUpdateItem.Enable()
		log.Printf("[Update] Check failed: %v", err)
		return
	}

	if info == nil {
		updateState = UpdateIdle
		updateInfo = nil
		updateAvailItem.Hide()
		checkUpdateItem.SetTitle("Up to date (" + version + ")")
		checkUpdateItem.Enable()
		return
	}

	updateState = UpdateAvailable
	updateInfo = info
	updateAvailItem.SetTitle("Update Available: " + info.Version)
	updateAvailItem.SetTooltip("Click to download and install version " + info.Version)
	updateAvailItem.Show()
	checkUpdateItem.SetTitle("Check for Updates")
	checkUpdateItem.Enable()

	showUpdateNotification(info.Version)
}

func manualUpdateCheck() {
	updateMu.Lock()
	if updateState == UpdateDownloading || updateState == UpdateChecking {
		updateMu.Unlock()
		return
	}
	updateMu.Unlock()

	doUpdateCheck()
}

func installUpdate() {
	updateMu.Lock()
	if updateInfo == nil {
		updateMu.Unlock()
		return
	}
	info := updateInfo

	if updateState == UpdateDownloading {
		updateMu.Unlock()
		return
	}
	updateState = UpdateDownloading
	updateMu.Unlock()

	updateAvailItem.SetTitle("Downloading update... 0%")
	updateAvailItem.Disable()
	checkUpdateItem.Disable()

	progressFn := func(percent int) {
		updateAvailItem.SetTitle(fmt.Sprintf("Downloading update... %d%%", percent))
	}

	err := performUpdate(info, progressFn)

	if err != nil {
		updateMu.Lock()
		updateState = UpdateFailed
		updateMu.Unlock()
		updateAvailItem.SetTitle("Update failed - click to retry")
		updateAvailItem.Enable()
		checkUpdateItem.Enable()
		log.Printf("[Update] Install failed: %v", err)
	}
}