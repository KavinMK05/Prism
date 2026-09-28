//go:build linux

package desktop

import "runtime"

// searxngPythonTarget is the python-build-standalone target triple used on Linux.
func searxngPythonTarget() string {
	if runtime.GOARCH == "arm64" {
		return "aarch64-unknown-linux-gnu"
	}
	return "x86_64-unknown-linux-gnu"
}

// searxngVenvPython returns the path to the venv's python interpreter on Linux.
func searxngVenvPython() string {
	return searxngVenvDir() + "/bin/python"
}

// searxngVenvPip returns the path to the venv's pip on Linux.
func searxngVenvPip() string {
	return searxngVenvDir() + "/bin/pip"
}

// searxngStandalonePythonBinary is the python binary path inside the extracted
// python-build-standalone tree, used to bootstrap the venv.
func searxngStandalonePythonBinary() string {
	return "bin/python3"
}

// systemPythonCandidates returns the interpreter names to look up on PATH when
// searching for a usable system Python (>=3.10). Most distributions ship
// python3; `python` covers PATH-configured installs.
func systemPythonCandidates() []string {
	return []string{"python3", "python"}
}
