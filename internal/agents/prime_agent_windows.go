//go:build windows

package agents

import (
	"os/exec"
	"syscall"
)

// createNoWindow maps to the Windows CREATE_NO_WINDOW process creation flag.
const createNoWindow = 0x08000000

// hideConsoleWindow prevents Windows from allocating a console window for a
// console-subsystem child process. Prism's tray binary is a GUI-subsystem
// executable (built with -H windowsgui), so it owns no console: when it starts
// a console child like wsl.exe without CREATE_NO_WINDOW, Windows creates a new
// console window, which flashes on screen for the life of the child. Every
// other Windows spawn in Prism goes through desktop's runHidden for the same
// reason; the WSL probe here lives in this package and needs its own copy.
func hideConsoleWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNoWindow,
	}
}
