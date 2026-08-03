//go:build darwin

package platform

import (
	"os"
	"path/filepath"
)

func ConfigDir() string {
	return filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "prism")
}

func LogDir() string {
	return filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "prism")
}
