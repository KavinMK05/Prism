//go:build linux

package platform

import "embed"

// Linux StatusNotifierItem / AppIndicator expects a PNG icon, so we embed a
// white glyph rather than the Windows .ico — most Linux top bars are dark, and
// SNI hosts do not recolor template icons the way macOS does.
//
//go:embed logo_icon_white.png
var iconFS embed.FS

func LoadIconData() ([]byte, error) {
	return embed.FS.ReadFile(iconFS, "logo_icon_white.png")
}
