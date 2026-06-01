//go:build windows

package main

import (
	"os"
	"path/filepath"
)

func getConfigDir() string {
	return filepath.Join(os.Getenv("APPDATA"), "prism")
}

func getLogDir() string {
	return filepath.Join(os.Getenv("APPDATA"), "prism")
}