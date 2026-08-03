//go:build windows

package platform

import "embed"

//go:embed logo_icon.ico
var iconFS embed.FS

func LoadIconData() ([]byte, error) {
	return embed.FS.ReadFile(iconFS, "logo_icon.ico")
}
