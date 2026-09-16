//go:build !windows

package agents

import "os/exec"

// hideConsoleWindow is a no-op outside Windows, which has no console-window
// allocation problem for spawned children. Kept signature-symmetric with the
// windows implementation.
func hideConsoleWindow(cmd *exec.Cmd) {}

// wslDefaultDistroRunning is always false outside Windows: there is no WSL to
// be running, and wslPrimeAgentDir returns early there. Kept
// signature-symmetric with the windows implementation.
func wslDefaultDistroRunning() bool { return false }
