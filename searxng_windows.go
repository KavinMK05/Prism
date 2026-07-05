//go:build windows

package main

// searxngPythonTarget is the python-build-standalone target triple used on
// Windows. The install_only assets are named without the "-shared" suffix even
// for shared ABI builds, so we use "x86_64-pc-windows-msvc" to match.
func searxngPythonTarget() string {
	return "x86_64-pc-windows-msvc"
}

// searxngVenvPython returns the path to the venv's python interpreter on Windows.
func searxngVenvPython() string {
	return searxngVenvDir() + "\\Scripts\\python.exe"
}

// searxngVenvPip returns the path to the venv's pip on Windows.
func searxngVenvPip() string {
	return searxngVenvDir() + "\\Scripts\\pip.exe"
}

// searxngStandalonePythonBinary is the python binary name inside the extracted
// python-build-standalone tree, used to bootstrap the venv.
func searxngStandalonePythonBinary() string {
	return "python.exe"
}

// systemPythonCandidates returns the interpreter names to look up on PATH when
// searching for a usable system Python (≥3.10). The Windows py launcher is
// listed first because it's the canonical install shim; `python`/`python3`
// cover PATH-configured installs.
func systemPythonCandidates() []string {
	return []string{"py", "python", "python3"}
}