//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows/registry"
)

func isAutoStartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.READ)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue("Prism")
	return err == nil
}

func setAutoStart(enable bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if enable {
		exePath, err := os.Executable()
		if err != nil {
			return err
		}
		return k.SetStringValue("Prism", exePath)
	}
	return k.DeleteValue("Prism")
}