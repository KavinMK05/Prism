//go:build darwin

package main

import "runtime"

// searxngPythonTarget is the python-build-standalone target triple used on macOS.
func searxngPythonTarget() string {
	if runtime.GOARCH == "arm64" {
		return "aarch64-apple-darwin"
	}
	return "x86_64-apple-darwin"
}

// searxngVenvPython returns the path to the venv's python interpreter on macOS.
func searxngVenvPython() string {
	return searxngVenvDir() + "/bin/python"
}

// searxngVenvPip returns the path to the venv's pip on macOS.
func searxngVenvPip() string {
	return searxngVenvDir() + "/bin/pip"
}

// searxngStandalonePythonBinary is the python binary path inside the extracted
// python-build-standalone tree, used to bootstrap the venv.
func searxngStandalonePythonBinary() string {
	return "bin/python3"
}

// systemPythonCandidates returns the interpreter names to look up on PATH when
// searching for a usable system Python (≥3.10). macOS ships python3 with the
// Xcode command-line tools; Homebrew also provides python3.
func systemPythonCandidates() []string {
	return []string{"python3", "python"}
}