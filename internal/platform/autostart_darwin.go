//go:build darwin

package platform

import (
	"fmt"
	"os"
	"path/filepath"
)

const launchAgentPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.prism</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
</dict>
</plist>`

// autostartPlistPath is the per-user LaunchAgent macOS starts at login.
func autostartPlistPath() string {
	return filepath.Join(os.Getenv("HOME"), "Library", "LaunchAgents", "com.prism.plist")
}

func IsAutoStartEnabled() bool {
	_, err := os.Stat(autostartPlistPath())
	return err == nil
}

func SetAutoStart(enable bool) error {
	plistPath := autostartPlistPath()
	if enable {
		exePath, err := os.Executable()
		if err != nil {
			return err
		}
		content := fmt.Sprintf(launchAgentPlist, exePath)
		os.MkdirAll(filepath.Dir(plistPath), 0755)
		return os.WriteFile(plistPath, []byte(content), 0644)
	}
	return os.Remove(plistPath)
}

// SyncAutoStartPath repoints the LaunchAgent at the running binary when it names
// a different copy of Prism - a build from a checkout, or an /Applications copy
// that a later install replaced. Auto-start the user never turned on (no plist)
// is deliberately left alone.
func SyncAutoStartPath() error {
	data, err := os.ReadFile(autostartPlistPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	exePath, err := os.Executable()
	if err != nil {
		return err
	}
	if sameExecutablePath(plistProgramPath(string(data)), exePath) {
		return nil
	}
	return SetAutoStart(true)
}
