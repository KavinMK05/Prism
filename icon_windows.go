//go:build windows

package main

import "embed"

//go:embed logo_icon.ico
var iconFS embed.FS

func loadIconData() ([]byte, error) {
	return embed.FS.ReadFile(iconFS, "logo_icon.ico")
}

func loadTemplateIconData() ([]byte, error) {
	return nil, nil
}