//go:build windows

package desktop

import "fyne.io/systray"

func setPlatformIcon(iconData []byte) {
	systray.SetIcon(iconData)
}
