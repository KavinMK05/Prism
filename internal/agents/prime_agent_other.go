//go:build !windows

package agents

import "os/exec"

// hideConsoleWindow is a no-op outside Windows, which has no console-window
// allocation problem for spawned children. Kept signature-symmetric with the
// windows implementation.
func hideConsoleWindow(cmd *exec.Cmd) {}
