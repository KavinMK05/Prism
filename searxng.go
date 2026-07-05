package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// fallbackPythonStandaloneTag is the pinned python-build-standalone release tag
// used when the latest-release metadata fetch fails. SearXNG requires Python ≥3.10;
// this tag ships a compatible CPython.
const fallbackPythonStandaloneTag = "20260324"

var (
	searxngPID       int
	searxngCmd       *exec.Cmd
	searxngMu        sync.Mutex
	searxngLogFile   *os.File
	searxngShouldRun bool
	searxngCrashTimes []time.Time

	searxngInstall    searxngInstallState
	searxngInstallMu  sync.Mutex
)

type searxngInstallState struct {
	Phase    string `json:"phase"`    // idle|downloading-python|extracting-python|creating-venv|installing-searxng|starting|running|error
	Progress int    `json:"progress"` // 0-100, -1 when not applicable
	Error    string `json:"error"`    // empty unless phase=="error"
}

// --- Path helpers (all under Prism's config dir) ---

func searxngDir() string {
	return filepath.Join(getConfigDir(), "searxng")
}

func searxngPythonDir() string {
	return filepath.Join(searxngDir(), "python")
}

func searxngVenvDir() string {
	return filepath.Join(searxngDir(), "venv")
}

func searxngSettingsPath() string {
	return filepath.Join(searxngDir(), "settings.yml")
}

// searxngSrcDir is the extracted SearXNG source tree. The webapp runs from here
// (python -m searx.webapp with cwd=src) so the `searx` package is importable.
func searxngSrcDir() string {
	return filepath.Join(searxngDir(), "src")
}

func searxngLogPath() string {
	return filepath.Join(getLogDir(), "searxng.log")
}

// --- Install state helpers ---

func setSearxngInstallPhase(phase string, progress int) {
	searxngInstallMu.Lock()
	searxngInstall.Phase = phase
	searxngInstall.Progress = progress
	searxngInstall.Error = ""
	searxngInstallMu.Unlock()
	updateSearxngTrayTitle()
}

func setSearxngInstallError(msg string) {
	searxngInstallMu.Lock()
	searxngInstall.Phase = "error"
	searxngInstall.Progress = -1
	searxngInstall.Error = msg
	searxngInstallMu.Unlock()
	updateSearxngTrayTitle()
}

// updateSearxngTrayTitle reflects the current install phase / running state in
// the tray status item. Nil-safe so non-tray callers don't crash.
func updateSearxngTrayTitle() {
	if searxStatusItem == nil {
		return
	}
	searxngInstallMu.Lock()
	st := searxngInstall
	searxngInstallMu.Unlock()
	switch st.Phase {
	case "idle", "running", "":
		// Sync tray menu (title + Start/Stop/Restart enabled state) to actual
		// running state. Covers admin-UI start/stop and autonomous process exits,
		// which don't go through the tray click handlers.
		updateSearxngMenu(isSearxngRunning())
	case "error":
		searxStatusItem.SetTitle("SearXNG: \u2716 Error")
	default:
		if st.Progress >= 0 {
			searxStatusItem.SetTitle(fmt.Sprintf("SearXNG: %s\u2026 %d%%", st.Phase, st.Progress))
		} else {
			searxStatusItem.SetTitle(fmt.Sprintf("SearXNG: %s\u2026", st.Phase))
		}
	}
}

// --- python-build-standalone release resolution ---

type pythonStandaloneAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

type pythonStandaloneRelease struct {
	TagName string                  `json:"tag_name"`
	Assets  []pythonStandaloneAsset `json:"assets"`
}

// pythonStandaloneLatestSummary is the schema of the latest-release.json file in
// the astral-sh/python-build-standalone repo. It does NOT contain an asset list —
// only the tag and a URL prefix — so the GitHub API release is still fetched to
// discover the exact asset filename (which embeds the Python version).
type pythonStandaloneLatestSummary struct {
	Tag            string `json:"tag"`
	AssetURLPrefix string `json:"asset_url_prefix"`
}

// fetchPythonRelease fetches and parses a python-build-standalone GitHub release
// (the GitHub API releases/tags/<tag> endpoint shape: tag_name + assets).
func fetchPythonRelease(tag string) (*pythonStandaloneRelease, error) {
	url := "https://api.github.com/repos/astral-sh/python-build-standalone/releases/tags/" + tag
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Prism/"+version)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api returned status %d for tag %s", resp.StatusCode, tag)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var rel pythonStandaloneRelease
	if err := json.Unmarshal(body, &rel); err != nil {
		return nil, fmt.Errorf("parse release json: %w", err)
	}
	if rel.TagName == "" {
		rel.TagName = tag
	}
	return &rel, nil
}

// resolvePythonRelease determines the latest python-build-standalone release and
// the install_only.tar.gz asset for the current platform target. Falls back to
// fallbackPythonStandaloneTag when the latest-release metadata is unreachable.
func resolvePythonRelease() (*pythonStandaloneAsset, error) {
	tag := ""
	if resp, err := http.Get("https://raw.githubusercontent.com/astral-sh/python-build-standalone/latest-release/latest-release.json"); err == nil {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			var summary pythonStandaloneLatestSummary
			if json.Unmarshal(body, &summary) == nil && summary.Tag != "" {
				tag = summary.Tag
			}
		}
	}
	if tag == "" {
		tag = fallbackPythonStandaloneTag
	}
	rel, err := fetchPythonRelease(tag)
	if err != nil {
		return nil, fmt.Errorf("fetch python release %s: %w", tag, err)
	}
	a := findPythonAsset(rel)
	if a == nil {
		return nil, fmt.Errorf("no python-build-standalone asset for target %s in release %s", searxngPythonTarget(), tag)
	}
	return a, nil
}

func findPythonAsset(rel *pythonStandaloneRelease) *pythonStandaloneAsset {
	suffix := searxngPythonTarget() + "-install_only.tar.gz"
	for i := range rel.Assets {
		if strings.HasSuffix(rel.Assets[i].Name, suffix) {
			return &rel.Assets[i]
		}
	}
	return nil
}

// --- Install ---

// resolveBootstrapPython returns a Python interpreter suitable for creating the
// SearXNG venv: the system Python if one ≥3.10 is on PATH, otherwise a freshly
// downloaded+extracted python-build-standalone interpreter. The returned path is
// the bootstrap interpreter; the venv then has its own isolated python/pip.
func resolveBootstrapPython(progressFn func(percent int)) (string, error) {
	if sys, err := findSystemPython(); err == nil {
		log.Printf("[SearXNG] using system python: %s", sys)
		return sys, nil
	}
	log.Printf("[SearXNG] no usable system python; downloading python-build-standalone")

	setSearxngInstallPhase("downloading-python", 0)
	asset, err := resolvePythonRelease()
	if err != nil {
		return "", err
	}
	tarPath := filepath.Join(searxngDir(), "python.tar.gz")
	if err := downloadFile(asset.BrowserDownloadURL, tarPath, progressFn); err != nil {
		setSearxngInstallError("download python: " + err.Error())
		return "", fmt.Errorf("download python: %w", err)
	}

	setSearxngInstallPhase("extracting-python", -1)
	if err := extractTarGz(tarPath, searxngPythonDir()); err != nil {
		setSearxngInstallError("extract python: " + err.Error())
		return "", fmt.Errorf("extract python: %w", err)
	}
	os.Remove(tarPath)

	bootstrap := filepath.Join(searxngPythonDir(), searxngStandalonePythonBinary())
	setSearxngInstallPhase("creating-venv", -1)
	return bootstrap, nil
}

// findSystemPython locates a system Python interpreter ≥3.10 on PATH. Returns
// the resolved binary path, or an error if none is found.
func findSystemPython() (string, error) {
	for _, c := range systemPythonCandidates() {
		path, err := exec.LookPath(c)
		if err != nil {
			continue
		}
		out, err := exec.Command(path, "--version").CombinedOutput()
		if err != nil {
			continue
		}
		ver := strings.TrimSpace(string(out))
		if strings.HasPrefix(ver, "Python ") {
			ver = ver[len("Python "):]
		}
		if systemPythonVersionOK(ver) {
			return path, nil
		}
	}
	return "", fmt.Errorf("no system python >=3.10 found on PATH")
}

// systemPythonVersionOK reports whether a "X.Y[.Z]" version string is ≥3.10.
func systemPythonVersionOK(ver string) bool {
	parts := strings.Split(ver, ".")
	if len(parts) < 2 {
		return false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	if major > 3 {
		return true
	}
	if major != 3 {
		return false
	}
	return minor >= 10
}

// downloadAndPrepareSearxngSource fetches the SearXNG source tarball from GitHub,
// extracts it (tolerant of Windows-illegal filenames), normalizes the top-level
// directory to searxngSrcDir(), and applies the Windows portability patch for
// the `pwd` import in valkeydb.py.
func downloadAndPrepareSearxngSource(progressFn func(percent int)) error {
	tarPath := filepath.Join(searxngDir(), "searxng.tar.gz")
	setSearxngInstallPhase("downloading-searxng", 0)
	url := "https://github.com/searxng/searxng/archive/refs/heads/master.tar.gz"
	if err := downloadFile(url, tarPath, progressFn); err != nil {
		return fmt.Errorf("download searxng source: %w", err)
	}

	setSearxngInstallPhase("extracting-searxng", -1)
	tmpExtract := filepath.Join(searxngDir(), "extract-tmp")
	os.RemoveAll(tmpExtract)
	if err := extractTarGz(tarPath, tmpExtract); err != nil {
		os.Remove(tarPath)
		return fmt.Errorf("extract searxng source: %w", err)
	}
	os.Remove(tarPath)

	// The tarball extracts to a single top-level dir (e.g. "searxng-master").
	entries, err := os.ReadDir(tmpExtract)
	if err != nil {
		os.RemoveAll(tmpExtract)
		return fmt.Errorf("read extract dir: %w", err)
	}
	var topDir string
	for _, e := range entries {
		if e.IsDir() {
			topDir = filepath.Join(tmpExtract, e.Name())
			break
		}
	}
	if topDir == "" {
		os.RemoveAll(tmpExtract)
		return fmt.Errorf("searxng source tarball has no top-level directory")
	}
	// Move the top-level dir to the canonical src location.
	os.RemoveAll(searxngSrcDir())
	if err := os.Rename(topDir, searxngSrcDir()); err != nil {
		os.RemoveAll(tmpExtract)
		return fmt.Errorf("move searxng source: %w", err)
	}
	os.RemoveAll(tmpExtract)

	patchSearxngForWindows(searxngSrcDir())
	return nil
}

// patchSearxngForWindows makes the `pwd` import in searx/valkeydb.py conditional.
// `pwd` is a Unix-only module; SearXNG imports it unconditionally at module load,
// which crashes the webapp on Windows before the limiter (which is off) is even
// consulted. The patch is a safe, idempotent text edit; on Unix it's a no-op
// (the try succeeds).
func patchSearxngForWindows(srcDir string) {
	p := filepath.Join(srcDir, "searx", "valkeydb.py")
	data, err := os.ReadFile(p)
	if err != nil {
		log.Printf("[SearXNG] valkeydb.py not found, skipping patch: %v", err)
		return
	}
	s := string(data)
	if !strings.Contains(s, "import pwd\n") {
		return // already patched or unexpected contents
	}
	s = strings.Replace(s, "import pwd\n", "try:\n    import pwd\nexcept ImportError:\n    pwd = None\n", 1)
	old := "        _pw = pwd.getpwuid(os.getuid())\n        logger.exception(\"[%s (%s)] can't connect valkey DB ...\", _pw.pw_name, _pw.pw_uid)"
	new := "        if pwd:\n            _pw = pwd.getpwuid(os.getuid())\n            logger.exception(\"[%s (%s)] can't connect valkey DB ...\", _pw.pw_name, _pw.pw_uid)\n        else:\n            logger.exception(\"can't connect valkey DB ...\")"
	s = strings.Replace(s, old, new, 1)
	if err := os.WriteFile(p, []byte(s), 0644); err != nil {
		log.Printf("[SearXNG] failed to write valkeydb.py patch: %v", err)
	}
}

// installSearxng downloads Python (python-build-standalone), creates a venv, and
// pip-installs searxng. Each step is idempotent so retries after partial failures
// skip completed work. progressFn receives download progress percentages.
func installSearxng(progressFn func(percent int)) error {
	if err := os.MkdirAll(searxngDir(), 0755); err != nil {
		return fmt.Errorf("create searxng dir: %w", err)
	}

	// Step 1+2: Python interpreter + venv. Prefer the system Python (venv isolates
	// SearXNG's packages); only download python-build-standalone when no usable
	// system interpreter is present.
	if _, err := os.Stat(searxngVenvPython()); err != nil {
		setSearxngInstallPhase("creating-venv", -1)
		bootstrapPython, err := resolveBootstrapPython(progressFn)
		if err != nil {
			setSearxngInstallError(err.Error())
			return err
		}
		cmd := runHidden(exec.Command(bootstrapPython, "-m", "venv", searxngVenvDir()))
		if out, err := cmd.CombinedOutput(); err != nil {
			setSearxngInstallError("create venv: " + err.Error() + " " + string(out))
			return fmt.Errorf("create venv: %w (%s)", err, strings.TrimSpace(string(out)))
		}
	}

	// Step 3: SearXNG source tree. The real engine is not on PyPI (the `searxng`
	// PyPI name is an unrelated MCP wrapper), so we fetch the source tarball and
	// run from source. Skip if already present.
	if _, err := os.Stat(filepath.Join(searxngSrcDir(), "searx", "__init__.py")); err != nil {
		if err := downloadAndPrepareSearxngSource(progressFn); err != nil {
			setSearxngInstallError(err.Error())
			return err
		}
	}

	// Step 4: pip install requirements into the venv (skip if a key dep is present).
	needInstall := false
	if _, err := os.Stat(searxngVenvPip()); err != nil {
		needInstall = true
	} else {
		check := runHidden(exec.Command(searxngVenvPip(), "show", "flask-babel"))
		if err := check.Run(); err != nil {
			needInstall = true
		}
	}
	if needInstall {
		setSearxngInstallPhase("installing-searxng", -1)
		installLogPath := filepath.Join(searxngDir(), "pip-install.log")
		installLog, err := os.OpenFile(installLogPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			setSearxngInstallError("open pip-install.log: " + err.Error())
			return fmt.Errorf("open pip-install.log: %w", err)
		}
		reqPath := filepath.Join(searxngSrcDir(), "requirements.txt")
		cmd := runHidden(exec.Command(searxngVenvPip(), "install", "-r", reqPath))
		cmd.Stdout = installLog
		cmd.Stderr = installLog
		if err := cmd.Run(); err != nil {
			installLog.Close()
			msg := fmt.Sprintf("pip install -r requirements.txt failed; see %s", installLogPath)
			setSearxngInstallError(msg)
			return fmt.Errorf("%s", msg)
		}
		// tzdata is not in requirements.txt (Linux ships it) but Windows needs it
		// for engines that use zoneinfo (e.g. bilibili). Safe no-op if already present.
		cmd2 := runHidden(exec.Command(searxngVenvPip(), "install", "tzdata"))
		cmd2.Stdout = installLog
		cmd2.Stderr = installLog
		_ = cmd2.Run()
		installLog.Close()
	}

	// Step 5: default settings.yml (only if absent — user owns it after first gen).
	if _, err := os.Stat(searxngSettingsPath()); err != nil {
		if err := writeDefaultSearxngSettings(); err != nil {
			setSearxngInstallError("write settings.yml: " + err.Error())
			return fmt.Errorf("write settings.yml: %w", err)
		}
	}

	setSearxngInstallPhase("idle", -1)
	return nil
}

// --- Lifecycle ---

func isSearxngRunning() bool {
	searxngMu.Lock()
	pid := searxngPID
	searxngMu.Unlock()
	return pidAlive(pid)
}

func searxngIsInstalled() bool {
	if _, err := os.Stat(searxngVenvPip()); err != nil {
		return false
	}
	if _, err := os.Stat(searxngSettingsPath()); err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(searxngSrcDir(), "searx", "__init__.py")); err != nil {
		return false
	}
	return true
}

func searxngAutostartEnabled() bool {
	return loadConfig().SearXNGAutoStart
}

func startSearxngProcess() error {
	if isSearxngRunning() {
		return nil
	}

	// Install on demand if the venv isn't ready.
	if !searxngIsInstalled() {
		if err := installSearxng(func(percent int) {
			searxngInstallMu.Lock()
			searxngInstall.Progress = percent
			searxngInstallMu.Unlock()
			updateSearxngTrayTitle()
		}); err != nil {
			return err
		}
	}

	logDir := getLogDir()
	os.MkdirAll(logDir, 0755)
	searxngMu.Lock()
	closeSearxngLogLocked()
	var err error
	searxngLogFile, err = os.OpenFile(searxngLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	searxngMu.Unlock()
	if err != nil {
		log.Printf("[SearXNG] Failed to open log file: %v", err)
		return fmt.Errorf("open searxng log: %w", err)
	}

	cmd := runHidden(exec.Command(searxngVenvPython(), "-m", "searx.webapp"))
	env := os.Environ()
	env = append(env, "SEARXNG_SETTINGS_PATH="+searxngSettingsPath())
	cmd.Env = env
	cmd.Dir = searxngSrcDir()
	cmd.Stdout = searxngLogFile
	cmd.Stderr = searxngLogFile

	if err := cmd.Start(); err != nil {
		searxngMu.Lock()
		searxngLogFile.Close()
		searxngLogFile = nil
		searxngMu.Unlock()
		setSearxngInstallError("start searxng: " + err.Error())
		return fmt.Errorf("start searxng: %w", err)
	}

	searxngMu.Lock()
	searxngPID = cmd.Process.Pid
	searxngCmd = cmd
	searxngShouldRun = true
	searxngMu.Unlock()

	setSearxngInstallPhase("running", -1)

	go func() {
		err := cmd.Wait()
		searxngMu.Lock()
		searxngPID = 0
		searxngCmd = nil
		closeSearxngLogLocked()
		shouldRun := searxngShouldRun
		searxngMu.Unlock()

		if err != nil && shouldRun {
			// Crash: prune crash timestamps older than 60s, record this one.
			searxngMu.Lock()
			now := time.Now()
			cutoff := now.Add(-60 * time.Second)
			pruned := searxngCrashTimes[:0]
			for _, t := range searxngCrashTimes {
				if t.After(cutoff) {
					pruned = append(pruned, t)
				}
			}
			pruned = append(pruned, now)
			searxngCrashTimes = pruned
			count := len(pruned)
			if count >= 5 {
				searxngShouldRun = false
				searxngMu.Unlock()
				log.Printf("[SearXNG] crashed repeatedly; stopping respawns")
				setSearxngInstallError("SearXNG crashed repeatedly; see searxng.log")
				return
			}
			searxngMu.Unlock()
			log.Printf("[SearXNG] crashed, restarting")
			time.Sleep(500 * time.Millisecond)
			_ = startSearxngProcess()
			return
		}

		// Clean exit or intentional stop.
		if err != nil {
			log.Printf("[SearXNG] exited with error: %v", err)
		}
		setSearxngInstallPhase("idle", -1)
	}()

	return nil
}

func closeSearxngLogLocked() {
	if searxngLogFile != nil {
		searxngLogFile.Close()
		searxngLogFile = nil
	}
}

func stopSearxngProcess() {
	searxngMu.Lock()
	searxngShouldRun = false
	pid := searxngPID
	if pid != 0 {
		stopProcessByPID(pid)
		searxngPID = 0
		searxngCmd = nil
	}
	closeSearxngLogLocked()
	searxngMu.Unlock()
	time.Sleep(300 * time.Millisecond)
	setSearxngInstallPhase("idle", -1)
}

func restartSearxngProcess() error {
	stopSearxngProcess()
	time.Sleep(500 * time.Millisecond)
	return startSearxngProcess()
}

// --- Status ---

func searxngStatus() map[string]interface{} {
	return map[string]interface{}{
		"running":   isSearxngRunning(),
		"port":      searxngPortFromSettings(),
		"install":   currentSearxngInstallState(),
		"autostart": searxngAutostartEnabled(),
		"installed": searxngIsInstalled(),
	}
}

func currentSearxngInstallState() searxngInstallState {
	searxngInstallMu.Lock()
	defer searxngInstallMu.Unlock()
	st := searxngInstall
	if st.Phase == "" {
		st.Phase = "idle"
	}
	if st.Progress == 0 && (st.Phase == "idle" || st.Phase == "running" || st.Phase == "error") {
		st.Progress = -1
	}
	return st
}
