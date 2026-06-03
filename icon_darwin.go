//go:build darwin

package main

import "embed"

//go:embed logo_icon_template.png
var iconFS embed.FS

func loadIconData() ([]byte, error) {
	return embed.FS.ReadFile(iconFS, "logo_icon_template.png")
}