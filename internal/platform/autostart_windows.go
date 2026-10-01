//go:build windows

package platform

import (
	"os"

	"golang.org/x/sys/windows/registry"
)

// autoStartRunKey is the per-user logon entry Windows launches at sign-in.
const autoStartRunKey = `Software\Microsoft\Windows\CurrentVersion\Run`

func IsAutoStartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, autoStartRunKey, registry.READ)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue("Prism")
	return err == nil
}

func SetAutoStart(enable bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, autoStartRunKey, registry.SET_VALUE)
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

// SyncAutoStartPath repoints an existing logon entry at the running binary.
//
// The value names the executable the user toggled auto-start from, so it goes
// stale as soon as that file is replaced or moved: a dev checkout, a portable
// copy or an earlier install path keeps winning at sign-in while the installed
// copy never starts. Auto-start the user never turned on (no value at all) is
// deliberately left alone.
func SyncAutoStartPath() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, autoStartRunKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()

	current, _, err := k.GetStringValue("Prism")
	if err != nil {
		// No value means auto-start is off, so there is nothing to sync.
		return nil
	}
	exePath, err := os.Executable()
	if err != nil {
		return err
	}
	if sameExecutablePath(current, exePath) {
		return nil
	}
	return k.SetStringValue("Prism", exePath)
}
