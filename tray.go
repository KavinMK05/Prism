package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/getlantern/systray"
)

var (
	cfg              *Config
	statusItem       *systray.MenuItem
	startItem        *systray.MenuItem
	stopItem         *systray.MenuItem
	mRestart         *systray.MenuItem
	apiKeyItem       *systray.MenuItem
	setKeyItem       *systray.MenuItem
	providerOllama   *systray.MenuItem
	providerOpenCode *systray.MenuItem
	providerCustom   *systray.MenuItem
	logFile          *os.File
	logFileMu        sync.Mutex
	proxyPID         int
)

const CREATE_NO_WINDOW = 0x08000000

func runHidden(cmd *exec.Cmd) *exec.Cmd {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: CREATE_NO_WINDOW,
	}
	return cmd
}

func runTray(iconData []byte) {
	cfg = loadConfig()

	systray.Run(func() {
		systray.SetIcon(iconData)
		systray.SetTooltip("Ollama Proxy")

		running := isProxyRunning()

		statusItem = systray.AddMenuItem("● Checking...", "Proxy status")
		statusItem.Disable()

		systray.AddSeparator()

		startItem = systray.AddMenuItem("Start Proxy", "Start the ollama-proxy")
		stopItem = systray.AddMenuItem("Stop Proxy", "Stop the ollama-proxy")
		mRestart = systray.AddMenuItem("Restart Proxy", "Restart the ollama-proxy")

		systray.AddSeparator()

		providerMenu := systray.AddMenuItem("Provider", "Select provider")
		providerOllama = providerMenu.AddSubMenuItem("Ollama Cloud", "Use Ollama Cloud")
		providerOpenCode = providerMenu.AddSubMenuItem("OpenCode Go", "Use OpenCode Go")
		providerCustom = providerMenu.AddSubMenuItem("Custom...", "Use custom provider")

		systray.AddSeparator()

		openFolderItem := systray.AddMenuItem("Open Folder", "Open proxy directory")
		editModelConfigItem := systray.AddMenuItem("Edit Model Config", "Open model remapping config in editor")
		showLogsItem := systray.AddMenuItem("Show Logs", "Open a console window with live logs")

		systray.AddSeparator()

		apiKeyItem = systray.AddMenuItem("API Key: "+maskKey(cfg.getActiveAPIKey()), "Current API key")
		apiKeyItem.Disable()
		setKeyItem = systray.AddMenuItem("Set API Key...", "Set the API key for the active provider")

		systray.AddSeparator()

		quitItem := systray.AddMenuItem("Quit", "Quit tray (stops proxy too)")

		updateMenu(running)
		updateProviderMenu()

		if !running {
			startProxyProcess()
			time.Sleep(500 * time.Millisecond)
			updateMenu(isProxyRunning())
		}

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
				case <-providerOllama.ClickedCh:
					cfg.ActiveProvider = "ollama_cloud"
					saveConfig(cfg)
					updateProviderMenu()
					updateAPIKeyDisplay()
					if isProxyRunning() {
						stopProxyProcess()
						time.Sleep(500 * time.Millisecond)
						startProxyProcess()
						time.Sleep(500 * time.Millisecond)
						updateMenu(isProxyRunning())
					}
				case <-providerOpenCode.ClickedCh:
					cfg.ActiveProvider = "opencode_go"
					saveConfig(cfg)
					updateProviderMenu()
					updateAPIKeyDisplay()
					if isProxyRunning() {
						stopProxyProcess()
						time.Sleep(500 * time.Millisecond)
						startProxyProcess()
						time.Sleep(500 * time.Millisecond)
						updateMenu(isProxyRunning())
					}
				case <-providerCustom.ClickedCh:
					url, err := showInputDialog("Custom Provider", "Enter the base URL for the API (e.g. https://api.example.com):", cfg.Custom.BaseURL)
					if err == nil && url != "" {
						if err := validateBaseURL(url); err != nil {
							log.Printf("Invalid custom provider URL: %v", err)
							continue
						}
						cfg.Custom.BaseURL = url
						cfg.ActiveProvider = "custom"
						saveConfig(cfg)
						updateProviderMenu()
						updateAPIKeyDisplay()
						if isProxyRunning() {
							stopProxyProcess()
							time.Sleep(500 * time.Millisecond)
							startProxyProcess()
							time.Sleep(500 * time.Millisecond)
							updateMenu(isProxyRunning())
						}
					}
				case <-openFolderItem.ClickedCh:
					exec.Command("explorer", filepath.Dir(getExePath())).Start()
				case <-editModelConfigItem.ClickedCh:
					editModelConfig()
				case <-showLogsItem.ClickedCh:
					openLogsConsole()
				case <-setKeyItem.ClickedCh:
					setAPIKey()
				case <-quitItem.ClickedCh:
					stopProxyProcess()
					closeLogFile()
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

func updateProviderMenu() {
	providerOllama.Uncheck()
	providerOpenCode.Uncheck()
	providerCustom.Uncheck()

	switch cfg.ActiveProvider {
	case "ollama_cloud":
		providerOllama.Check()
	case "opencode_go":
		providerOpenCode.Check()
	case "custom":
		providerCustom.Check()
	}

	providerName := cfg.getActiveProvider().Name
	systray.SetTooltip("Ollama Proxy · " + providerName)
}

func updateAPIKeyDisplay() {
	key := cfg.getActiveAPIKey()
	apiKeyItem.SetTitle("API Key: " + maskKey(key))
}

func setAPIKey() {
	p := cfg.getActiveProvider()
	title := "Set API Key - " + p.Name
	prompt := "Enter API key for " + p.Name + ":"
	defaultVal := p.APIKey

	key, err := showInputDialog(title, prompt, defaultVal)
	if err != nil || key == "" {
		return
	}

	p.APIKey = key
	saveConfig(cfg)
	updateAPIKeyDisplay()

	if isProxyRunning() {
		stopProxyProcess()
		time.Sleep(500 * time.Millisecond)
		startProxyProcess()
		time.Sleep(500 * time.Millisecond)
		updateMenu(isProxyRunning())
	}
}

func startProxyProcess() {
	if isProxyRunning() {
		return
	}
	logFileMu.Lock()
	closeLogFileLocked()

	logDir := filepath.Join(os.Getenv("APPDATA"), "ollama-proxy")
	os.MkdirAll(logDir, 0755)
	logPath := filepath.Join(logDir, "proxy.log")
	var err error
	logFile, err = os.Create(logPath)
	if err != nil {
		logFileMu.Unlock()
		log.Printf("Failed to create log file: %v", err)
		return
	}
	logFileMu.Unlock()

	exe := getExePath()
	cmd := runHidden(exec.Command(exe, "--serve"))
	cmd.Env = append(os.Environ(), "OLLAMA_API_KEY="+cfg.getActiveAPIKey())
	cmd.Dir = filepath.Dir(exe)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		log.Printf("Failed to start proxy: %v", err)
		return
	}
	proxyPID = cmd.Process.Pid
}

func stopProxyProcess() {
	if proxyPID != 0 {
		runHidden(exec.Command("taskkill", "/PID", fmt.Sprintf("%d", proxyPID), "/F")).Run()
		proxyPID = 0
	}
	time.Sleep(300 * time.Millisecond)
	closeLogFileMutex()
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

func getExePath() string {
	exe, err := os.Executable()
	if err != nil {
		return "ollama-proxy.exe"
	}
	return exe
}

func isProxyRunning() bool {
	if proxyPID == 0 {
		return false
	}
	proc, err := os.FindProcess(proxyPID)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}

func getLogFilePath() string {
	return filepath.Join(os.Getenv("APPDATA"), "ollama-proxy", "proxy.log")
}

func openLogsConsole() {
	logPath := getLogFilePath()
	cmd := exec.Command("powershell", "-Command",
		fmt.Sprintf("Get-Content -Path '%s' -Wait -Tail 50", strings.ReplaceAll(logPath, "'", "''")))
	cmd.Start()
}

func editModelConfig() {
	remapPath := getModelRemappingPath()
	if _, err := os.Stat(remapPath); os.IsNotExist(err) {
		remap := defaultModelRemapping()
		saveModelRemapping(remap)
	}

	cmd := exec.Command("notepad", remapPath)
	if err := cmd.Start(); err != nil {
		log.Printf("Failed to open model config editor: %v", err)
		return
	}

	go func() {
		cmd.Wait()
		cfg = loadConfig()
		if isProxyRunning() {
			stopProxyProcess()
			time.Sleep(500 * time.Millisecond)
			startProxyProcess()
			time.Sleep(500 * time.Millisecond)
			updateMenu(isProxyRunning())
		}
	}()
}