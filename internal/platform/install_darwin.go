//go:build darwin

package platform

// MSIInstallDir is always empty on macOS: the updater replaces the whole
// Prism.app bundle in place, so there is no installer-owned state to reconcile.
func MSIInstallDir() string { return "" }
