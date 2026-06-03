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
	oauthMenu        *systray.MenuItem
	providerCustomSlots []*systray.MenuItem
	providerCustomIDs   []string
	providerCustomIDsMu sync.RWMutex
	oauthSlots          []*systray.MenuItem
	oauthIDs            []string
	oauthIDsMu          sync.RWMutex
	logFile          *os.File
	logFileMu        sync.Mutex
	proxyPID         int
	proxyCmd         *exec.Cmd
	proxyRunningMu   sync.Mutex
)

const maxCustomProviders = 10
const maxOAuthAccounts = 10

func getAdminPort() string {
	port := os.Getenv("PRISM_ADMIN_PORT")
	if port == "" {
		port = "8765"
	}
	return port
}

func runTray(iconData []byte, templateIconData []byte) {
	cfg = loadConfig()

	// Start the admin UI server in the tray process
	startAdminServer(cfg, getAdminPort())

	systray.Run(func() {
		systray.SetTemplateIcon(templateIconData, iconData)
		systray.SetTooltip("Prism")

		running := isProxyRunning()

		statusItem = systray.AddMenuItem("● Checking...", "Proxy status")
		statusItem.Disable()

		systray.AddSeparator()

		startItem = systray.AddMenuItem("Start Proxy", "Start Prism proxy")
		stopItem = systray.AddMenuItem("Stop Proxy", "Stop Prism proxy")
		mRestart = systray.AddMenuItem("Restart Proxy", "Restart Prism proxy")

		systray.AddSeparator()

		providerMenu := systray.AddMenuItem("Default Provider", "Select default provider")
		providerOllama = providerMenu.AddSubMenuItem("Ollama Cloud", "Use Ollama Cloud")
		providerOpenCode = providerMenu.AddSubMenuItem("OpenCode Go", "Use OpenCode Go")

		// Pre-allocate slots for custom providers (hidden until needed)
		providerCustomSlots = make([]*systray.MenuItem, maxCustomProviders)
		providerCustomIDs = make([]string, maxCustomProviders)
		for i := 0; i < maxCustomProviders; i++ {
			item := providerMenu.AddSubMenuItem("", "")
			item.Hide()
			providerCustomSlots[i] = item
		}

		providerCustom = providerMenu.AddSubMenuItem("Manage Custom...", "Open settings to manage custom providers")

		// OAuth accounts submenu
		oauthMenu = providerMenu.AddSubMenuItem("OAuth Accounts", "Manage OAuth accounts")
		oauthSlots = make([]*systray.MenuItem, maxOAuthAccounts)
		oauthIDs = make([]string, maxOAuthAccounts)
		for i := 0; i < maxOAuthAccounts; i++ {
			item := oauthMenu.AddSubMenuItem("", "")
			item.Hide()
			oauthSlots[i] = item
		}
		addCodexItem := oauthMenu.AddSubMenuItem("+ Add Codex Account...", "Connect a ChatGPT/Codex account")
		refreshUsageItem := oauthMenu.AddSubMenuItem("↻ Refresh Usage", "Refresh usage data for all accounts")

		systray.AddSeparator()

		openSettingsItem := systray.AddMenuItem("Open Settings", "Open web-based settings panel")
		openFolderItem := systray.AddMenuItem("Open Folder", "Open proxy directory")
		editModelConfigItem := systray.AddMenuItem("Edit Model Config", "Open model remapping config in editor")
		showLogsItem := systray.AddMenuItem("Show Logs", "Open a console window with live logs")

		systray.AddSeparator()

		apiKeyItem = systray.AddMenuItem("API Key: "+maskKey(cfg.getDefaultAPIKey()), "Current API key")
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

		// Start background usage refresh for OAuth accounts
		startUsageRefreshLoop()

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
					cfg.DefaultProvider = "ollama_cloud"
					// Clear Active flags on all OAuth accounts since we switched away
					for _, a := range cfg.OAuthAccounts {
						a.Active = false
					}
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
					cfg.DefaultProvider = "opencode_go"
					// Clear Active flags on all OAuth accounts since we switched away
					for _, a := range cfg.OAuthAccounts {
						a.Active = false
					}
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
					// Open the web settings UI to manage custom providers
					openAdminUI(getAdminPort())
				case <-addCodexItem.ClickedCh:
					// Start Codex OAuth flow
					go func() {
						_, err := startCodexOAuth()
						if err != nil {
							log.Printf("[Tray] Failed to start Codex OAuth: %v", err)
						}
					}()
				case <-refreshUsageItem.ClickedCh:
					// Refresh usage for all accounts
					go func() {
						adminMu.Lock()
						cfgCopy := adminConfig
						adminMu.Unlock()
						for _, a := range cfgCopy.OAuthAccounts {
							if a.Provider == "codex" && a.AccessToken != "" {
								if _, err := refreshUsageForAccount(a); err != nil {
									log.Printf("[Tray] Usage refresh failed for %s: %v", a.Email, err)
								}
							}
						}
						notifyTrayConfigChanged()
					}()
				case <-openSettingsItem.ClickedCh:
					openAdminUI(getAdminPort())
				case <-openFolderItem.ClickedCh:
					openInFileExplorer(filepath.Dir(getExePath()))
				case <-editModelConfigItem.ClickedCh:
					editModelConfig()
				case <-showLogsItem.ClickedCh:
					openLogsConsole()
				case <-setKeyItem.ClickedCh:
					// Open web settings for API key management
					openAdminUI(getAdminPort())
				case <-quitItem.ClickedCh:
					stopProxyProcess()
					closeLogFile()
					systray.Quit()
				}
			}
		}()

		// Click handlers for custom provider slots
		for i := 0; i < maxCustomProviders; i++ {
			slotIdx := i
			go func() {
				for range providerCustomSlots[slotIdx].ClickedCh {
					providerCustomIDsMu.RLock()
					id := providerCustomIDs[slotIdx]
					providerCustomIDsMu.RUnlock()
					if id == "" {
						continue
					}
					cfg.DefaultProvider = id
					// Clear Active flags on all OAuth accounts since we switched away
					for _, a := range cfg.OAuthAccounts {
						a.Active = false
					}
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
			}()
		}

		// Click handlers for OAuth account slots
		for i := 0; i < maxOAuthAccounts; i++ {
			slotIdx := i
			go func() {
				for range oauthSlots[slotIdx].ClickedCh {
					oauthIDsMu.RLock()
					id := oauthIDs[slotIdx]
					oauthIDsMu.RUnlock()
					if id == "" {
						continue
					}
					cfg.DefaultProvider = id
					// Set Active flag on the selected account and clear others
					for _, a := range cfg.OAuthAccounts {
						a.Active = (a.ID == id)
					}
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
			}()
		}
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

	// Update custom provider slots
	providerCustomIDsMu.Lock()
	for i := 0; i < maxCustomProviders; i++ {
		if i < len(cfg.CustomProviders) {
			p := cfg.CustomProviders[i]
			providerCustomIDs[i] = p.ID
			providerCustomSlots[i].SetTitle(p.Name)
			providerCustomSlots[i].SetTooltip("Use " + p.Name)
			providerCustomSlots[i].Show()
			if cfg.DefaultProvider == p.ID {
				providerCustomSlots[i].Check()
			} else {
				providerCustomSlots[i].Uncheck()
			}
		} else {
			providerCustomIDs[i] = ""
			providerCustomSlots[i].Hide()
		}
	}
	providerCustomIDsMu.Unlock()

	// Update OAuth account slots
	oauthIDsMu.Lock()
	for i := 0; i < maxOAuthAccounts; i++ {
		if i < len(cfg.OAuthAccounts) {
			a := cfg.OAuthAccounts[i]
			oauthIDs[i] = a.ID
			label := a.Email
			if label == "" {
				label = a.ID
			}
			if a.PlanTier != "" {
				label += " (" + a.PlanTier + ")"
			}
			// Add usage percentage if available
			if a.CreditsTotal > 0 {
				pct := (a.CreditsUsed / a.CreditsTotal) * 100
				label += fmt.Sprintf(" [%.0f%% used]", pct)
			}
			oauthSlots[i].SetTitle(label)
			oauthSlots[i].SetTooltip("Switch to " + a.Email)
			oauthSlots[i].Show()
			if cfg.DefaultProvider == a.ID {
				oauthSlots[i].Check()
			} else {
				oauthSlots[i].Uncheck()
			}
		} else {
			oauthIDs[i] = ""
			oauthSlots[i].Hide()
		}
	}
	oauthIDsMu.Unlock()

	switch cfg.DefaultProvider {
	case "ollama_cloud":
		providerOllama.Check()
	case "opencode_go":
		providerOpenCode.Check()
	default:
		// Check if active is an OAuth account
		for _, a := range cfg.OAuthAccounts {
			if cfg.DefaultProvider == a.ID {
				// Already checked in the slot loop above
			}
		}
		// Check if active is a custom provider — already checked in the slot loop above
	}

	providerName := cfg.getDefaultProvider().Name
	systray.SetTooltip("Prism · " + providerName)
}

func updateAPIKeyDisplay() {
	key := cfg.getDefaultAPIKey()
	apiKeyItem.SetTitle("API Key: " + maskKey(key))
}

func setAPIKey() {
	info, err := cfg.getProviderByID(cfg.DefaultProvider)
	if err != nil {
		return
	}
	title := "Set API Key - " + info.Name
	prompt := "Enter API key for " + info.Name + ":"
	defaultVal := info.APIKey

	key, err := showInputDialog(title, prompt, defaultVal)
	if err != nil || key == "" {
		return
	}

	// Update the API key for the default provider
	switch cfg.DefaultProvider {
	case "ollama_cloud":
		cfg.OllamaCloud.APIKey = key
	case "opencode_go":
		cfg.OpenCodeGo.APIKey = key
	default:
		for _, p := range cfg.CustomProviders {
			if p.ID == cfg.DefaultProvider {
				p.APIKey = key
				break
			}
		}
	}
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