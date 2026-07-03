package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	githubOwner = "monoes"
	githubRepo  = "mono-agent"
)

// VersionInfo is returned by GetVersion.
type VersionInfo struct {
	Version   string `json:"version"`
	BuildDate string `json:"build_date"`
}

// UpdateInfo is returned by CheckForUpdate.
type UpdateInfo struct {
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version"`
	UpdateAvailable bool   `json:"update_available"`
	ReleaseURL      string `json:"release_url"`
	Error           string `json:"error,omitempty"`
}

// UpdateResult is returned by SelfUpdate.
type UpdateResult struct {
	Success    bool   `json:"success"`
	NewVersion string `json:"new_version,omitempty"`
	Error      string `json:"error,omitempty"`
}

// GetVersion returns the current build version.
func (a *App) GetVersion() VersionInfo {
	return VersionInfo{
		Version:   version,
		BuildDate: buildDate,
	}
}

// CheckForUpdate queries GitHub for the latest release and compares.
func (a *App) CheckForUpdate() UpdateInfo {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", githubOwner, githubRepo)
	req, _ := http.NewRequest("GET", apiURL, nil)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return UpdateInfo{CurrentVersion: version, Error: fmt.Sprintf("network error: %v", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return UpdateInfo{CurrentVersion: version, Error: fmt.Sprintf("GitHub API %d: %s", resp.StatusCode, string(body))}
	}

	var release struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return UpdateInfo{CurrentVersion: version, Error: fmt.Sprintf("parse error: %v", err)}
	}

	latest := strings.TrimPrefix(release.TagName, "v")
	current := strings.TrimPrefix(version, "v")

	return UpdateInfo{
		CurrentVersion:  version,
		LatestVersion:   release.TagName,
		UpdateAvailable: latest != current && version != "dev",
		ReleaseURL:      release.HTMLURL,
	}
}

// SelfUpdate downloads the latest release binary and replaces the CLI.
// The UI app shows a dialog to restart after update.
func (a *App) SelfUpdate() UpdateResult {
	// 1. Get latest release info
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", githubOwner, githubRepo)
	req, _ := http.NewRequest("GET", apiURL, nil)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return UpdateResult{Error: fmt.Sprintf("network error: %v", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return UpdateResult{Error: fmt.Sprintf("GitHub API %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))}
	}

	var release struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return UpdateResult{Error: fmt.Sprintf("parse error: %v", err)}
	}

	// 2. Find the right asset for this platform
	assetName := cliAssetName()
	var downloadURL string
	for _, asset := range release.Assets {
		if asset.Name == assetName {
			downloadURL = asset.BrowserDownloadURL
			break
		}
	}
	if downloadURL == "" {
		return UpdateResult{Error: fmt.Sprintf("no binary found for %s/%s (expected %s)", goruntime.GOOS, goruntime.GOARCH, assetName)}
	}

	// 3. Find the CLI binary path
	cliPath, err := findCLIBinary()
	if err != nil {
		return UpdateResult{Error: fmt.Sprintf("cannot locate CLI binary: %v", err)}
	}

	// 4. Download to temp file
	runtime.EventsEmit(a.ctx, "update:progress", "Downloading update...")
	dlResp, err := http.Get(downloadURL)
	if err != nil {
		return UpdateResult{Error: fmt.Sprintf("download error: %v", err)}
	}
	defer dlResp.Body.Close()

	tmpFile, err := os.CreateTemp("", "monoagentcli-update-*")
	if err != nil {
		return UpdateResult{Error: fmt.Sprintf("temp file error: %v", err)}
	}
	tmpPath := tmpFile.Name()

	if _, err := io.Copy(tmpFile, dlResp.Body); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return UpdateResult{Error: fmt.Sprintf("download write error: %v", err)}
	}
	tmpFile.Close()

	// 5. Replace the CLI binary
	runtime.EventsEmit(a.ctx, "update:progress", "Installing update...")
	if err := os.Chmod(tmpPath, 0755); err != nil {
		os.Remove(tmpPath)
		return UpdateResult{Error: fmt.Sprintf("chmod error: %v", err)}
	}

	// Atomic replace: rename old → .bak, rename new → target, remove .bak
	bakPath := cliPath + ".bak"
	os.Remove(bakPath) // clean up any previous backup
	if err := os.Rename(cliPath, bakPath); err != nil {
		os.Remove(tmpPath)
		return UpdateResult{Error: fmt.Sprintf("backup error: %v", err)}
	}
	if err := os.Rename(tmpPath, cliPath); err != nil {
		// Rollback
		os.Rename(bakPath, cliPath)
		return UpdateResult{Error: fmt.Sprintf("install error: %v", err)}
	}
	os.Remove(bakPath)

	runtime.EventsEmit(a.ctx, "update:progress", "Update complete!")
	return UpdateResult{
		Success:    true,
		NewVersion: release.TagName,
	}
}

// cliAssetName returns the expected GitHub release asset name for the current OS/arch.
func cliAssetName() string {
	switch goruntime.GOOS {
	case "darwin":
		if goruntime.GOARCH == "arm64" {
			return "monoagentcli-darwin-arm64"
		}
		return "monoagentcli-darwin-amd64"
	case "linux":
		return "monoagentcli-linux-amd64"
	case "windows":
		return "monoagentcli-windows-amd64.exe"
	default:
		return "monoagentcli-" + goruntime.GOOS + "-" + goruntime.GOARCH
	}
}

// backgroundUpdateCheck runs once on startup (after a short delay) and then
// every 24 hours, emitting "update:available" when a newer release exists.
func (a *App) backgroundUpdateCheck() {
	time.Sleep(10 * time.Second)
	if info := a.CheckForUpdate(); info.UpdateAvailable {
		runtime.EventsEmit(a.ctx, "update:available", info)
	}

	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if info := a.CheckForUpdate(); info.UpdateAvailable {
				runtime.EventsEmit(a.ctx, "update:available", info)
			}
		case <-a.ctx.Done():
			return
		}
	}
}

// findCLIBinary locates the monoagentcli binary.
func findCLIBinary() (string, error) {
	candidates := []string{}

	if p, err := exec.LookPath("monoagentcli"); err == nil {
		candidates = append(candidates, p)
	}

	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates, filepath.Join(dir, "monoagentcli"))
		candidates = append(candidates, filepath.Join(dir, "..", "bin", "monoagentcli"))
	}

	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, "go", "bin", "monoagentcli"))
		candidates = append(candidates, filepath.Join(home, ".local", "bin", "monoagentcli"))
	}
	candidates = append(candidates, "/usr/local/bin/monoagentcli")

	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c, nil
		}
	}

	return "", fmt.Errorf("monoagentcli binary not found in PATH or common locations")
}
