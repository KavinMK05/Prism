package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

const (
	githubOwner   = "KavinMK05"
	githubRepo    = "Prism"
	githubAPIURL  = "https://api.github.com/repos/" + githubOwner + "/" + githubRepo + "/releases/latest"
	updateCheckInterval = 24 * time.Hour
)

// GitHubRelease represents the relevant fields from the GitHub Releases API response.
type GitHubRelease struct {
	TagName string              `json:"tag_name"`
	HTMLURL string              `json:"html_url"`
	Assets  []GitHubReleaseAsset `json:"assets"`
}

// GitHubReleaseAsset represents a single asset in a GitHub release.
type GitHubReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL  string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

// UpdateState tracks the current state of an update flow.
type UpdateState int

const (
	UpdateIdle UpdateState = iota
	UpdateChecking
	UpdateAvailable
	UpdateDownloading
	UpdateReady
	UpdateFailed
)

// UpdateInfo holds information about an available update.
type UpdateInfo struct {
	Version    string
	DownloadURL string
	AssetName  string
	AssetSize  int64
	ReleaseURL string
}

// fetchLatestRelease queries the GitHub API for the latest release.
func fetchLatestRelease() (*GitHubRelease, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest("GET", githubAPIURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", "Prism/"+version)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API returned %d: %s", resp.StatusCode, string(body))
	}

	var release GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("decode release: %w", err)
	}
	return &release, nil
}

// compareVersions compares two semver version strings (without "v" prefix).
// Returns: 1 if b > a (update available), 0 if equal, -1 if a > b.
func compareVersions(a, b string) int {
	a = strings.TrimPrefix(a, "v")
	b = strings.TrimPrefix(b, "v")

	partsA := strings.Split(a, ".")
	partsB := strings.Split(b, ".")

	maxLen := len(partsA)
	if len(partsB) > maxLen {
		maxLen = len(partsB)
	}

	for i := 0; i < maxLen; i++ {
		var numA, numB int
		if i < len(partsA) {
			fmt.Sscanf(partsA[i], "%d", &numA)
		}
		if i < len(partsB) {
			fmt.Sscanf(partsB[i], "%d", &numB)
		}
		if numB > numA {
			return 1
		}
		if numB < numA {
			return -1
		}
	}
	return 0
}

// checkForUpdate checks GitHub for a newer release and returns update info if available.
func checkForUpdate() (*UpdateInfo, error) {
	log.Printf("[Update] Checking for updates (current: %s)...", version)

	release, err := fetchLatestRelease()
	if err != nil {
		log.Printf("[Update] Check failed: %v", err)
		return nil, err
	}

	latestVersion := release.TagName
	if compareVersions(version, latestVersion) <= 0 {
		log.Printf("[Update] Already up to date (current: %s, latest: %s)", version, latestVersion)
		return nil, nil
	}

	assetName := getUpdateAssetName()
	downloadURL := ""
	var assetSize int64

	for _, asset := range release.Assets {
		if asset.Name == assetName {
			downloadURL = asset.BrowserDownloadURL
			assetSize = asset.Size
			break
		}
	}

	if downloadURL == "" {
		log.Printf("[Update] Asset %q not found in release %s", assetName, latestVersion)
		return nil, fmt.Errorf("asset %q not found in release", assetName)
	}

	log.Printf("[Update] Update available: %s -> %s", version, latestVersion)

	return &UpdateInfo{
		Version:     latestVersion,
		DownloadURL: downloadURL,
		AssetName:   assetName,
		AssetSize:   assetSize,
		ReleaseURL:  release.HTMLURL,
	}, nil
}

// downloadFile downloads a file from url to destPath, calling progressFn with percentage.
func downloadFile(url, destPath string, progressFn func(percent int)) error {
	client := &http.Client{Timeout: 10 * time.Minute}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", "Prism/"+version)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	totalSize := resp.ContentLength
	if totalSize <= 0 {
		totalSize = 0
	}

	out, err := createDestFile(destPath)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer out.Close()

	var downloaded int64
	buf := make([]byte, 32*1024)
	lastPercent := -1

	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			written, writeErr := out.Write(buf[:n])
			if writeErr != nil {
				return fmt.Errorf("write file: %w", writeErr)
			}
			downloaded += int64(written)

			if totalSize > 0 && progressFn != nil {
				percent := int(float64(downloaded) / float64(totalSize) * 100)
				if percent != lastPercent {
					lastPercent = percent
					progressFn(percent)
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read response: %w", err)
		}
	}

	if progressFn != nil {
		progressFn(100)
	}

	return nil
}

// showUpdateNotification displays a platform-specific notification about an available update.
func showUpdateNotification(newVersion string) {
	showPlatformNotification("Prism Update Available", "Version "+newVersion+" is ready to install. Click the tray icon to update.")
}
