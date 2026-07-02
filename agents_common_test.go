package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestLookupBinaryFindsAgentOffPath simulates the macOS GUI-app scenario: the
// process PATH is minimal (a .app bundle launched from Finder inherits only
// /usr/bin:/bin), and the agent binary lives in ~/.bun/bin — a directory GUI
// apps don't inherit. lookupBinary must still find it via the curated install
// dirs so the agent isn't wrongly reported as "not installed".
func TestLookupBinaryFindsAgentOffPath(t *testing.T) {
	origPATH := os.Getenv("PATH")
	origHome := homeEnvValue()
	defer func() {
		os.Setenv("PATH", origPATH)
		setHomeEnv(origHome)
	}()

	tmpHome := t.TempDir()
	binDir := filepath.Join(tmpHome, ".bun", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	binName := "fake-agent"
	binPath := filepath.Join(binDir, binName)
	if runtime.GOOS == "windows" {
		binPath += ".exe"
	}
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write bin: %v", err)
	}

	// Minimal PATH so exec.LookPath cannot find it via PATH.
	if err := os.Setenv("PATH", "/usr/bin:/bin"); err != nil {
		t.Fatalf("setenv PATH: %v", err)
	}
	setHomeEnv(tmpHome)

	p, ok := lookupBinary(binName)
	if !ok || p == "" {
		t.Fatalf("lookupBinary(%q) not found under minimal PATH with ~/.bun/bin fallback", binName)
	}
	if p != binPath {
		t.Fatalf("lookupBinary(%q) = %q, want %q", binName, p, binPath)
	}
}

func homeEnvValue() string {
	if v := os.Getenv("HOME"); v != "" {
		return v
	}
	return os.Getenv("USERPROFILE")
}

func setHomeEnv(v string) {
	if runtime.GOOS == "windows" {
		os.Setenv("USERPROFILE", v)
	} else {
		os.Setenv("HOME", v)
	}
}