//go:build darwin

package main

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

func isAutoStartEnabled() bool {
	plistPath := filepath.Join(os.Getenv("HOME"), "Library", "LaunchAgents", "com.prism.plist")
	_, err := os.Stat(plistPath)
	return err == nil
}

func setAutoStart(enable bool) error {
	plistPath := filepath.Join(os.Getenv("HOME"), "Library", "LaunchAgents", "com.prism.plist")
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