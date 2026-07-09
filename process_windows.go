//go:build windows

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const CREATE_NO_WINDOW = 0x08000000
const STILL_ACTIVE = 259
const PROCESS_QUERY_LIMITED_INFORMATION = 0x1000

func findPIDsOnPort(port string) []int {
	out, err := exec.Command("netstat", "-ano").Output()
	if err != nil {
		return nil
	}
	seen := map[int]bool{}
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		addr := fields[1]
		state := fields[3]
		if !strings.HasSuffix(addr, ":"+port) || state != "LISTENING" {
			continue
		}
		pid, err := strconv.Atoi(fields[4])
		if err != nil || pid == 0 || seen[pid] {
			continue
		}
		seen[pid] = true
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
		runHidden(exec.Command("taskkill", "/PID", fmt.Sprintf("%d", pid), "/F")).Run()
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
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: CREATE_NO_WINDOW,
	}
	return cmd
}

func stopProxyProcess() {
	proxyRunningMu.Lock()
	if proxyPID != 0 {
		runHidden(exec.Command("taskkill", "/PID", fmt.Sprintf("%d", proxyPID), "/F")).Run()
		proxyPID = 0
		proxyCmd = nil
	}
	proxyRunningMu.Unlock()
	time.Sleep(300 * time.Millisecond)
	closeLogFileMutex()
}

// stopProcessByPID terminates a process by PID (Windows taskkill /F).
func stopProcessByPID(pid int) {
	runHidden(exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/F")).Run()
}

func pidAlive(pid int) bool {
	if pid == 0 {
		return false
	}
	handle, err := syscall.OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(handle)
	var exitCode uint32
	err = syscall.GetExitCodeProcess(handle, &exitCode)
	if err != nil {
		return false
	}
	return exitCode == STILL_ACTIVE
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
		return "prism.exe"
	}
	return exe
}