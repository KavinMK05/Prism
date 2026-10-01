//go:build windows

package platform

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// markerKey is the registry key the Prism MSI writes its install state to. A
// per-user package writes under HKCU, a per-machine package would write under
// HKLM. This path and the value names below are a contract with
// installer/windows/Prism.wxs and with install kind detection in shipped
// binaries - changing either silently turns MSI installs back into "portable".
const markerKey = `Software\Prism`

// MSIInstallDir reports the directory a Prism MSI installed into, or "" when
// no MSI install of Prism is registered (portable builds, developer checkouts,
// and releases that predate the installer).
func MSIInstallDir() string {
	if dir := msiInstallDir(registry.CURRENT_USER, registry.QUERY_VALUE); dir != "" {
		return dir
	}
	// A per-machine package records the same values under HKLM. Ask for the
	// 64-bit view explicitly so a 32-bit build still sees a 64-bit package.
	return msiInstallDir(registry.LOCAL_MACHINE, registry.QUERY_VALUE|registry.WOW64_64KEY)
}

func msiInstallDir(root registry.Key, access uint32) string {
	k, err := registry.OpenKey(root, markerKey, access)
	if err != nil {
		return ""
	}
	defer k.Close()

	installType, _, err := k.GetStringValue("InstallType")
	if err != nil || !strings.EqualFold(installType, "msi") {
		return ""
	}

	dir, _, err := k.GetStringValue("InstallDir")
	if err != nil {
		return ""
	}
	return filepath.Clean(dir)
}
