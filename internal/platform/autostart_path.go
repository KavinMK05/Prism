package platform

import (
	"path/filepath"
	"runtime"
	"strings"
)

// This file holds the platform-independent half of auto-start handling so the
// parsers and the path comparison can be unit-tested on any host.

// sameExecutablePath reports whether two spellings of an executable path refer
// to the same file.
//
// Auto-start entries are stored as free text: Windows quotes the Run value when
// the path contains spaces, macOS wraps it in plist tags and Linux puts it in a
// .desktop Exec= line. A raw string compare would report a difference on every
// launch and rewrite the entry forever.
func sameExecutablePath(a, b string) bool {
	clean := func(p string) string {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, `"`)
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		return filepath.Clean(p)
	}
	a, b = clean(a), clean(b)
	if runtime.GOOS == "windows" {
		// Windows paths are case-insensitive and the registry can hold either
		// case; elsewhere a differing case is a genuinely different file.
		return strings.EqualFold(a, b)
	}
	return a == b
}

// plistProgramPath returns the executable inside the ProgramArguments array of
// the generated LaunchAgent plist. The Label value is also a <string> element,
// so the scan waits for the array before reading one.
func plistProgramPath(content string) string {
	inArray := false
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "<array>" {
			inArray = true
			continue
		}
		if !inArray {
			continue
		}
		if strings.HasPrefix(line, "<string>") && strings.HasSuffix(line, "</string>") {
			return strings.TrimSuffix(strings.TrimPrefix(line, "<string>"), "</string>")
		}
	}
	return ""
}

// desktopExecPath returns the command from the Exec= line of the generated
// autostart entry. The whole remainder of the line is compared, so a path
// containing spaces still matches what SetAutoStart wrote.
func desktopExecPath(content string) string {
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "Exec=") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Exec="))
		}
	}
	return ""
}
