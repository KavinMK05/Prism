//go:build linux

package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// AcquireInstanceLock takes an exclusive, non-blocking flock on a lock file
// inside ConfigDir(). The returned release function unlocks and removes the
// lock file. If another Prism instance already holds the lock an error is
// returned.
func AcquireInstanceLock() (func(), error) {
	lockPath := filepath.Join(ConfigDir(), "prism.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0755); err != nil {
		return nil, err
	}

	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, err
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("prism is already running")
	}

	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
		os.Remove(lockPath)
	}, nil
}
