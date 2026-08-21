package desktop

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// SearXNG self-update support. SearXNG is a rolling release with no versioned
// tags; Prism installs from the master branch tarball. To know whether an
// installed copy is stale we record the exact master commit the source was
// downloaded at (searxngDir()/commit.txt) and compare it against the current
// master HEAD via the GitHub API.

const searxngMasterCommitURL = "https://api.github.com/repos/searxng/searxng/commits/master"

// searxngUpdateMu serializes updates and lets StartSearxngProcess refuse to
// run (and potentially re-install on demand) while a source refresh is midway.
var searxngUpdateMu sync.Mutex

func searxngCommitPath() string {
	return filepath.Join(searxngDir(), "commit.txt")
}

// fetchSearxngMasterCommit returns the current HEAD commit SHA of SearXNG's
// master branch.
func fetchSearxngMasterCommit() (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest("GET", searxngMasterCommitURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Prism/"+version)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github api returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var commit struct {
		Sha string `json:"sha"`
	}
	if err := json.Unmarshal(body, &commit); err != nil {
		return "", fmt.Errorf("parse commit json: %w", err)
	}
	if commit.Sha == "" {
		return "", fmt.Errorf("github api returned no sha")
	}
	return commit.Sha, nil
}

// readInstalledSearxngCommit returns the recorded source commit, or "" when
// unknown (installs made before commit tracking existed).
func readInstalledSearxngCommit() string {
	data, err := os.ReadFile(searxngCommitPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// writeInstalledSearxngCommit records the commit the source tree was fetched at.
func writeInstalledSearxngCommit(commit string) {
	if err := os.WriteFile(searxngCommitPath(), []byte(commit+"\n"), 0644); err != nil {
		log.Printf("[SearXNG] failed to record installed commit: %v", err)
	}
}

// clearInstalledSearxngCommit drops a stale commit marker so the update check
// doesn't compare against a source tree that no longer matches it.
func clearInstalledSearxngCommit() {
	os.Remove(searxngCommitPath())
}

// SearxngUpdateStatus describes whether the installed SearXNG source differs
// from upstream master.
type SearxngUpdateStatus struct {
	UpdateAvailable bool   `json:"update_available"`
	InstalledCommit string `json:"installed_commit"` // "" when unknown (pre-tracking install)
	LatestCommit    string `json:"latest_commit"`
	InProgress      bool   `json:"in_progress"`
}

// SearxngCheckUpdate compares the installed source commit with upstream
// master HEAD. An install without a recorded commit (created before tracking,
// or where the API was unreachable during install) is reported as an available
// update, since refreshing it is always safe and brings it current.
func SearxngCheckUpdate() (*SearxngUpdateStatus, error) {
	if !SearxngIsInstalled() {
		return nil, fmt.Errorf("SearXNG is not installed")
	}
	latest, err := fetchSearxngMasterCommit()
	if err != nil {
		return nil, fmt.Errorf("check for updates: %w", err)
	}
	installed := readInstalledSearxngCommit()
	return &SearxngUpdateStatus{
		UpdateAvailable: installed == "" || !strings.EqualFold(installed, latest),
		InstalledCommit: installed,
		LatestCommit:    latest,
		InProgress:      SearxngUpdateInProgress(),
	}, nil
}

// SearxngUpdateInProgress reports whether an update is currently running.
func SearxngUpdateInProgress() bool {
	if searxngUpdateMu.TryLock() {
		searxngUpdateMu.Unlock()
		return false
	}
	return true
}

// UpdateSearxng refreshes the bundled SearXNG source to the current master
// HEAD and reinstalls requirements (dependency pins change between snapshots).
// If SearXNG was running it is stopped first and restarted afterwards. The
// venv, Python interpreter, and settings.yml are left untouched.
func UpdateSearxng() error {
	searxngUpdateMu.Lock()
	wasRunning := isSearxngRunning()
	if wasRunning {
		log.Printf("[SearXNG] update: stopping running instance")
		StopSearxngProcess()
	}
	if err := func() error {
		defer searxngUpdateMu.Unlock()

		if !SearxngIsInstalled() {
			return fmt.Errorf("SearXNG is not installed")
		}
		if err := downloadAndPrepareSearxngSource(nil); err != nil {
			setSearxngInstallError(err.Error())
			return err
		}
		// Dependency pins may have changed between snapshots; always reinstall.
		setSearxngInstallPhase("installing-searxng", -1)
		if err := pipInstallRequirements(); err != nil {
			setSearxngInstallError(err.Error())
			return err
		}
		setSearxngInstallPhase("idle", -1)
		return nil
	}(); err != nil {
		return err
	}

	// Lock released — safe to start again (StartSearxngProcess refuses while
	// an update holds the lock).
	if wasRunning {
		return StartSearxngProcess()
	}
	return nil
}
