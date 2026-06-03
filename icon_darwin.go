//go:build darwin

package main

import "embed"

//go:embed logo_icon.png
var iconFS embed.FS

//go:embed logo_icon_template.png
var templateIconFS embed.FS

func loadIconData() ([]byte, error) {
	return embed.FS.ReadFile(iconFS, "logo_icon.png")
}

func loadTemplateIconData() ([]byte, error) {
	return embed.FS.ReadFile(templateIconFS, "logo_icon_template.png")
}