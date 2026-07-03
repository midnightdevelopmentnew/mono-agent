package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

func newUpdateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Update monoagentcli to the latest release",
		RunE:  runUpdate,
	}
}

func runUpdate(_ *cobra.Command, _ []string) error {
	fmt.Println("Checking for updates...")

	apiURL := "https://api.github.com/repos/monoes/mono-agent/releases/latest"
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("network error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GitHub API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var release struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	latest := strings.TrimPrefix(release.TagName, "v")
	current := strings.TrimPrefix(version, "v")

	if version == "dev" || latest == current {
		fmt.Printf("Already on latest version (%s)\n", version)
		return nil
	}
	fmt.Printf("Update available: %s → %s\n", version, release.TagName)

	assetName := updateAssetName()
	var downloadURL string
	for _, a := range release.Assets {
		if a.Name == assetName {
			downloadURL = a.BrowserDownloadURL
			break
		}
	}
	if downloadURL == "" {
		return fmt.Errorf("no binary for %s/%s in release (wanted %s)", runtime.GOOS, runtime.GOARCH, assetName)
	}

	selfPath, err := selfBinaryPath()
	if err != nil {
		return fmt.Errorf("locate binary: %w", err)
	}

	fmt.Printf("Downloading %s...\n", assetName)
	dlResp, err := http.Get(downloadURL) //nolint:gosec
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer dlResp.Body.Close()

	tmp, err := os.CreateTemp("", "monoagentcli-update-*")
	if err != nil {
		return fmt.Errorf("temp file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := io.Copy(tmp, dlResp.Body); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write download: %w", err)
	}
	tmp.Close()

	if err := os.Chmod(tmpPath, 0755); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("chmod: %w", err)
	}

	bak := selfPath + ".bak"
	os.Remove(bak)
	if err := os.Rename(selfPath, bak); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("backup: %w", err)
	}
	if err := os.Rename(tmpPath, selfPath); err != nil {
		os.Rename(bak, selfPath) // rollback
		return fmt.Errorf("install: %w", err)
	}
	os.Remove(bak)

	fmt.Printf("Updated to %s\n", release.TagName)
	return nil
}

func updateAssetName() string {
	switch runtime.GOOS {
	case "darwin":
		if runtime.GOARCH == "arm64" {
			return "monoagentcli-darwin-arm64"
		}
		return "monoagentcli-darwin-amd64"
	case "linux":
		return "monoagentcli-linux-amd64"
	case "windows":
		return "monoagentcli-windows-amd64.exe"
	default:
		return "monoagentcli-" + runtime.GOOS + "-" + runtime.GOARCH
	}
}

func selfBinaryPath() (string, error) {
	// Try resolving via argv[0] first (most reliable for installed binaries)
	arg0 := os.Args[0]
	if p, err := exec.LookPath(arg0); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs, nil
		}
	}
	if abs, err := filepath.Abs(arg0); err == nil {
		if _, err := os.Stat(abs); err == nil {
			return abs, nil
		}
	}
	return "", fmt.Errorf("cannot locate own binary (argv[0]=%s)", arg0)
}
