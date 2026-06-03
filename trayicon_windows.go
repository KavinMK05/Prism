//go:build windows

package main

import "github.com/getlantern/systray"

func setPlatformIcon(iconData []byte) {
	systray.SetIcon(iconData)
}
