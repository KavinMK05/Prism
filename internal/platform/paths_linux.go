//go:build linux

package platform

import (
	"os"
	"path/filepath"
)

// ConfigDir returns the XDG config directory for Prism:
// $XDG_CONFIG_HOME/prism, defaulting to ~/.config/prism.
func ConfigDir() string {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(configHome, "prism")
}

// LogDir returns the directory holding Prism's log files.
func LogDir() string {
	return filepath.Join(ConfigDir(), "logs")
}
