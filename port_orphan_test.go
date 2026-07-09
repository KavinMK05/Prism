package main

import (
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

// TestPortOrphanHelper is the helper-subprocess half of TestKillOrphansOnPort.
// When invoked with PORT_ORPHAN_HELPER_PORT set, it binds a listening socket on
// 127.0.0.1:<port> and blocks forever on Accept, ignoring errors, so the parent
// test can discover its PID via findPIDsOnPort and then kill it. It never
// returns while the env var is present; it only exits when the parent kills it.
func TestPortOrphanHelper(t *testing.T) {
	port := os.Getenv("PORT_ORPHAN_HELPER_PORT")
	if port == "" {
		t.Skip("helper subprocess only; run via TestKillOrphansOnPort")
	}
	t.Logf("helper binding 127.0.0.1:%s pid=%d", port, os.Getpid())

	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		// Stay alive regardless: the parent polls for our PID on the port and
		// will time out + kill us. Never return, or the testing framework would
		// mark us done and the process would exit before the parent can act.
		t.Logf("helper listen failed: %v", err)
		select {}
	}
	defer ln.Close()

	for {
		conn, err := ln.Accept()
		if err != nil {
			continue
		}
		_ = conn.Close()
	}
}

// TestKillOrphansOnPort exercises killOrphansOnPort against a real foreign
// listener spawned as a child test process. It verifies that the helper skips
// the known PID (Test A) and kills a foreign listener when knownPID is 0
// (Test B).
func TestKillOrphansOnPort(t *testing.T) {
	if os.Getenv("PORT_ORPHAN_HELPER_PORT") != "" {
		t.Skip("not run inside helper subprocess")
	}

	// Pick a free ephemeral port, then release it for the child to bind.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("close free port listener: %v", err)
	}
	portStr := strconv.Itoa(port)

	cmd := exec.Command(os.Args[0], "-test.run=TestPortOrphanHelper", "-test.v")
	cmd.Env = append(os.Environ(),
		"PORT_ORPHAN_HELPER_PORT="+portStr,
		"GO_WANT_HELPER_PROCESS=1",
	)
	// Keep the child's test output out of the parent's parsed output stream.
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper subprocess: %v", err)
	}
	childPID := cmd.Process.Pid
	t.Logf("spawned helper pid=%d on port %d", childPID, port)

	// Reap the child when it exits so it doesn't linger as a zombie.
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	defer func() {
		if pidAlive(childPID) {
			_ = cmd.Process.Kill()
		}
		<-done
	}()

	// Wait for the child to actually be listening so findPIDsOnPort can see it.
	found := false
	for i := 0; i < 20; i++ {
		for _, pid := range findPIDsOnPort(portStr) {
			if pid == childPID {
				found = true
				break
			}
		}
		if found {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !found {
		t.Fatalf("helper pid=%d never bound to port %d", childPID, port)
	}
	t.Logf("helper is listening on port %d", port)

	// Test A: knownPID == child must skip the child and kill nothing.
	if n := killOrphansOnPort(portStr, childPID); n != 0 {
		t.Fatalf("Test A: killOrphansOnPort(%q, %d) = %d, want 0", portStr, childPID, n)
	}
	if !pidAlive(childPID) {
		t.Fatalf("Test A: child pid=%d died but should have been skipped", childPID)
	}
	t.Logf("Test A passed: child pid=%d skipped and still alive", childPID)

	// Test B: knownPID == 0 must kill the foreign listener (the child).
	if n := killOrphansOnPort(portStr, 0); n < 1 {
		t.Fatalf("Test B: killOrphansOnPort(%q, 0) = %d, want >=1", portStr, n)
	}
	// Wait (bounded) for the child to actually exit after taskkill/kill -9.
	exited := false
	for i := 0; i < 50; i++ {
		if !pidAlive(childPID) {
			exited = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !exited {
		t.Fatalf("Test B: child pid=%d still alive after killOrphansOnPort(0)", childPID)
	}
	t.Logf("Test B passed: child pid=%d killed, returned count >=1", childPID)

	<-done
}