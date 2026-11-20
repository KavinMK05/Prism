//go:build windows

package config

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockHandle blocks until this process holds an exclusive lock on f. Windows
// releases the lock when the handle closes, so a crashed writer cannot strand
// it. The byte range is arbitrary: only the lock's exclusivity matters.
func lockHandle(f *os.File) error {
	overlapped := new(windows.Overlapped)
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK,
		0, 1, 0,
		overlapped,
	)
}

func unlockHandle(f *os.File) error {
	overlapped := new(windows.Overlapped)
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, overlapped)
}
