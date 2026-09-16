//go:build windows

package agents

import (
	"os/exec"
	"testing"
	"time"
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

// TestWSLDefaultDistroRunningDoesNotBootDistro guards the regression that made
// every Prism startup boot the user's WSL distro - ~5s of startup and ~1.8GB of
// utility-VM memory - merely to find out where Prime Agent keeps its config.
// The check has to stay a metadata query, so a cold boot shows up as a slow
// call: `wsl -l -v` answers in ~75ms, while booting the distro takes seconds.
func TestWSLDefaultDistroRunningDoesNotBootDistro(t *testing.T) {
	if wslExe() == "" {
		t.Skip("wsl.exe not available")
	}
	start := time.Now()
	running := wslDefaultDistroRunning()
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Errorf("wslDefaultDistroRunning() took %v; it must not boot the distro", elapsed)
	}
	t.Logf("default distro already running = %v (checked in %v)", running, elapsed)
}

// TestWSLDefaultDistroNameMatchesWSLList checks the registry read agrees with
// wsl.exe, since the running check compares the two. Both sides are
// metadata-only, so neither starts anything.
func TestWSLDefaultDistroNameMatchesWSLList(t *testing.T) {
	if wslExe() == "" {
		t.Skip("wsl.exe not available")
	}
	name := wslDefaultDistroName()
	if name == "" {
		t.Skip("no default distro recorded in the registry")
	}
	cmd := exec.Command(wslExe(), "--list", "--quiet")
	hideConsoleWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("wsl --list failed: %v", err)
	}
	if !wslDistroInList(parseWSLDistroList(out), name) {
		t.Errorf("registry default distro %q is not in `wsl --list`", name)
	}
}
