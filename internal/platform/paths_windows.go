//go:build windows

package platform

import (
	"os"
	"path/filepath"
)

func ConfigDir() string {
	return filepath.Join(os.Getenv("APPDATA"), "prism")
}

func LogDir() string {
	return filepath.Join(os.Getenv("APPDATA"), "prism")
}
