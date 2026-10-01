package desktop

import (
	"os"
	"path/filepath"
	"strings"

	"ollama-proxy/internal/platform"
)

// InstalledViaMSI reports whether this process is running from the directory a
// Prism MSI installed into.
//
// The install directory recorded by the package is compared against the
// running executable rather than merely checking for the registry marker, so a
// portable prism.exe on a machine that also has an MSI install is still treated
// as portable.
//
// The distinction decides how updates are applied: prism.exe in an MSI install
// lives in a directory Windows Installer owns, so replacing it behind the
// installer's back would make the next repair or upgrade roll the file back to
// the version the installed product still believes it is. MSI installs go
// through msiexec instead (see performUpdate in update_windows.go).
func InstalledViaMSI() bool {
	installDir := platform.MSIInstallDir()
	if installDir == "" {
		return false
	}
	exePath, err := os.Executable()
	if err != nil {
		return false
	}
	return strings.EqualFold(filepath.Clean(filepath.Dir(exePath)), installDir)
}
