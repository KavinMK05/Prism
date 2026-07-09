//go:build darwin

package main

import (
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func findPIDsOnPort(port string) []int {
	out, err := exec.Command("lsof", "-i", ":"+port, "-t").Output()
	if err != nil {
		return nil
	}
	var pids []int
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pid, err := strconv.Atoi(line)
		if err != nil {
			continue
		}
		pids = append(pids, pid)
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
		exec.Command("kill", "-9", strconv.Itoa(pid)).Run()
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

func runHidden(cmd *exec.Cmd) *exec.Cmd {
	return cmd
}

func stopProxyProcess() {
	proxyRunningMu.Lock()
	if proxyPID != 0 {
		exec.Command("kill", strconv.Itoa(proxyPID)).Run()
		proxyPID = 0
		proxyCmd = nil
	}
	proxyRunningMu.Unlock()
	time.Sleep(300 * time.Millisecond)
	closeLogFileMutex()
}

// stopProcessByPID terminates a process by PID (macOS kill).
func stopProcessByPID(pid int) {
	exec.Command("kill", strconv.Itoa(pid)).Run()
}

func pidAlive(pid int) bool {
	if pid == 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

func isProxyRunning() bool {
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