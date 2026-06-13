//go:build darwin

package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
)

func acquireInstanceLock() (func(), error) {
	lockPath := filepath.Join(getConfigDir(), "prism.lock")
	os.MkdirAll(filepath.Dir(lockPath), 0755)

	ln, err := net.Listen("unix", lockPath)
	if err != nil {
		// Check if another instance is actually listening
		conn, dialErr := net.Dial("unix", lockPath)
		if dialErr != nil {
			// Socket file is stale (no one listening) — remove and retry
			os.Remove(lockPath)
			ln, err = net.Listen("unix", lockPath)
			if err != nil {
				return nil, fmt.Errorf("prism is already running")
			}
		} else {
			conn.Close()
			return nil, fmt.Errorf("prism is already running")
		}
	}
	return func() {
		ln.Close()
		os.Remove(lockPath)
	}, nil
}