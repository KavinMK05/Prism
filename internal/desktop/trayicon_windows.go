//go:build windows

package desktop

import "github.com/getlantern/systray"

func setPlatformIcon(iconData []byte) {
	systray.SetIcon(iconData)
}
