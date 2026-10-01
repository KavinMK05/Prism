//go:build linux

package platform

// MSIInstallDir is always empty on Linux: the updater replaces the binary or
// AppImage in place, so there is no installer-owned state to reconcile.
func MSIInstallDir() string { return "" }
