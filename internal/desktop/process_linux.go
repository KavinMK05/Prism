//go:build linux

package desktop

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// findPIDsOnPort returns the PIDs of processes listening on the given TCP port.
// It matches LISTEN socket inodes from /proc/net/tcp{,6} against the socket
// symlinks in /proc/<pid>/fd.
func findPIDsOnPort(port string) []int {
	portNum, err := strconv.Atoi(port)
	if err != nil {
		return nil
	}
	want := uint16(portNum)

	inodes := map[string]bool{}
	for _, procNet := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		data, err := os.ReadFile(procNet)
		if err != nil {
			continue
		}
		lines := strings.Split(string(data), "\n")
		for _, line := range lines[1:] {
			fields := strings.Fields(line)
			if len(fields) < 10 {
				continue
			}
			// fields[1] = local_address "HEXIP:HEXPORT"
			// fields[3] = state (0A == TCP_LISTEN)
			// fields[9] = socket inode
			addr := fields[1]
			idx := strings.LastIndex(addr, ":")
			if idx < 0 {
				continue
			}
			p, err := strconv.ParseUint(addr[idx+1:], 16, 16)
			if err != nil || uint16(p) != want {
				continue
			}
			if fields[3] != "0A" {
				continue
			}
			inodes[fields[9]] = true
		}
	}
	if len(inodes) == 0 {
		return nil
	}

	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	seen := map[int]bool{}
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		fdDir := filepath.Join("/proc", e.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil {
				continue
			}
			if !strings.HasPrefix(link, "socket:[") {
				continue
			}
			inode := strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")
			if inodes[inode] && !seen[pid] {
				seen[pid] = true
				pids = append(pids, pid)
			}
		}
	}
	return pids
}

// killOrphansOnPort terminates every process listening on port that isn't
// knownPID, returning how many it killed. Used to reclaim a port held by an
// orphaned child from a prior Prism run before binding a fresh server.
func killOrphansOnPort(port string, knownPID int) int {
	var killed int
	for _, pid := range findPIDsOnPort(port) {
		if pid == knownPID {
			continue
		}
		log.Printf("Killing orphaned process %d on port %s", pid, port)
		syscall.Kill(pid, syscall.SIGKILL)
		killed++
		time.Sleep(300 * time.Millisecond)
	}
	return killed
}

func killOrphanOnPort() {
	port := os.Getenv("PRISM_PORT")
	if port == "" {
		port = "11434"
	}

	proxyRunningMu.Lock()
	knownPID := proxyPID
	proxyRunningMu.Unlock()

	killOrphansOnPort(port, knownPID)
}

// runHidden is the identity on Linux: there is no console window to hide.
func runHidden(cmd *exec.Cmd) *exec.Cmd {
	return cmd
}

func StopProxyProcess() {
	proxyRunningMu.Lock()
	if proxyPID != 0 {
		syscall.Kill(proxyPID, syscall.SIGTERM)
		proxyPID = 0
		proxyCmd = nil
	}
	proxyRunningMu.Unlock()
	time.Sleep(300 * time.Millisecond)
	closeLogFileMutex()
}

// stopProcessByPID terminates a process by PID (Linux SIGTERM).
func stopProcessByPID(pid int) {
	syscall.Kill(pid, syscall.SIGTERM)
}

func pidAlive(pid int) bool {
	if pid == 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

func IsProxyRunning() bool {
	proxyRunningMu.Lock()
	pid := proxyPID
	proxyRunningMu.Unlock()
	return pidAlive(pid)
}

func getExePath() string {
	exe, err := os.Executable()
	if err != nil {
		return "prism"
	}
	return exe
}
