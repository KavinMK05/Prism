//go:build darwin

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
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

func killOrphanOnPort() {
	port := os.Getenv("PRISM_PORT")
	if port == "" {
		port = "11434"
	}

	proxyRunningMu.Lock()
	knownPID := proxyPID
	proxyRunningMu.Unlock()

	for _, pid := range findPIDsOnPort(port) {
		if pid == knownPID {
			continue
		}
		log.Printf("Killing orphaned process %d on port %s", pid, port)
		exec.Command("kill", "-9", strconv.Itoa(pid)).Run()
		time.Sleep(300 * time.Millisecond)
	}
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

func isProxyRunning() bool {
	proxyRunningMu.Lock()
	pid := proxyPID
	proxyRunningMu.Unlock()
	if pid == 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

func getExePath() string {
	exe, err := os.Executable()
	if err != nil {
		return "prism"
	}
	return exe
}