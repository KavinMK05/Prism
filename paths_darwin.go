//go:build darwin

package main

import (
	"os"
	"path/filepath"
)

func getConfigDir() string {
	return filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "prism")
}

func getLogDir() string {
	return filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "prism")
}