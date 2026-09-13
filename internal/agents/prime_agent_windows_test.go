//go:build windows

package agents

import (
	"os/exec"
	"testing"
)

// TestHideConsoleWindowSetsCreateNoWindow guards the regression that made the
// Prime Agent WSL probe flash a console window on every Prism startup: the
// GUI-subsystem tray binary must pass CREATE_NO_WINDOW when spawning the
// console-subsystem wsl.exe.
func TestHideConsoleWindowSetsCreateNoWindow(t *testing.T) {
	cmd := exec.Command("cmd")
	hideConsoleWindow(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("hideConsoleWindow left SysProcAttr nil")
	}
	if cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Errorf("CreationFlags = %#x, want CREATE_NO_WINDOW (%#x) set",
			cmd.SysProcAttr.CreationFlags, createNoWindow)
	}
}
