//go:build darwin

package platform

import "embed"

//go:embed logo_icon_template.png
var iconFS embed.FS

func LoadIconData() ([]byte, error) {
	return embed.FS.ReadFile(iconFS, "logo_icon_template.png")
}
