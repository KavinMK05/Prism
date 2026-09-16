//go:build windows

package agents

import (
	"context"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/windows/registry"
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

// wslDefaultDistroRunning reports whether the default WSL distro is already
// running, without starting it.
//
// `wsl --list --running --quiet` is a metadata-only query: it answers in ~75ms
// and leaves a stopped distro - and its utility VM - stopped. That is the
// whole point of this function, because every other way in (the sh probe, or
// merely stat-ing the \\wsl$ share) boots the distro.
func wslDefaultDistroRunning() bool {
	wsl := wslExe()
	if wsl == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, wsl, "--list", "--running", "--quiet")
	// Console-subsystem binary, same as the probe below.
	hideConsoleWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	names := parseWSLDistroList(out)
	if len(names) == 0 {
		// Nothing is running, so WSL and its utility VM are down. Bail out here:
		// anything below this point is allowed to boot them.
		return false
	}
	if name := wslDefaultDistroName(); name != "" {
		return wslDistroInList(names, name)
	}
	// The registry read failed (unusual WSL install), but something is already
	// running, so the utility VM is up and the probe cannot cold-boot it again.
	return true
}

// wslDefaultDistroName reads the default distro's name from the registry, which
// starts nothing: HKCU\...\Lxss\DefaultDistribution holds the distro's GUID and
// the matching subkey holds its DistributionName. Returns "" when unreadable.
func wslDefaultDistroName() string {
	const lxss = `Software\Microsoft\Windows\CurrentVersion\Lxss`
	k, err := registry.OpenKey(registry.CURRENT_USER, lxss, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	guid, _, err := k.GetStringValue("DefaultDistribution")
	if err != nil || guid == "" {
		return ""
	}
	sub, err := registry.OpenKey(registry.CURRENT_USER, lxss+`\`+guid, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer sub.Close()
	name, _, err := sub.GetStringValue("DistributionName")
	if err != nil {
		return ""
	}
	return name
}
