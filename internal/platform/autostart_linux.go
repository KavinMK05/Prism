//go:build linux

package platform

import (
	"fmt"
	"os"
	"path/filepath"
)

// autostartDesktopEntry is the freedesktop.org autostart entry written to
// ~/.config/autostart/prism.desktop when auto-start is enabled.
const autostartDesktopEntry = `[Desktop Entry]
Type=Application
Name=Prism
Comment=Prism local AI proxy
Exec=%s
Terminal=false
X-GNOME-Autostart-enabled=true
`

// autostartFilePath returns the path to the freedesktop autostart entry,
// honouring $XDG_CONFIG_HOME (defaulting to ~/.config).
func autostartFilePath() string {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(configHome, "autostart", "prism.desktop")
}

func IsAutoStartEnabled() bool {
	_, err := os.Stat(autostartFilePath())
	return err == nil
}

func SetAutoStart(enable bool) error {
	path := autostartFilePath()
	if enable {
		exePath, err := os.Executable()
		if err != nil {
			return err
		}
		content := fmt.Sprintf(autostartDesktopEntry, exePath)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		return os.WriteFile(path, []byte(content), 0644)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
