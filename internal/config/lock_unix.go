//go:build !windows

package config

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockHandle blocks until this process holds an exclusive flock on f. The
// kernel releases it when the file is closed or the process exits, so a crashed
// writer cannot strand the lock.
func lockHandle(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX)
}

func unlockHandle(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
