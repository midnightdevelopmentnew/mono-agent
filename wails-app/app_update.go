package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	goruntime "runtime"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// AppSelfUpdate downloads the latest Wails app release and performs an
// in-place update, then restarts the app. The UI app (not just the CLI
// sidecar) is replaced.
func (a *App) AppSelfUpdate() UpdateResult {
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

	assetName := appAssetName()
	if assetName == "" {
		return UpdateResult{Error: fmt.Sprintf("app update not supported on %s/%s", goruntime.GOOS, goruntime.GOARCH)}
	}

	var downloadURL string
	for _, asset := range release.Assets {
		if asset.Name == assetName {
			downloadURL = asset.BrowserDownloadURL
			break
		}
	}
	if downloadURL == "" {
		return UpdateResult{Error: fmt.Sprintf("no app asset for %s/%s (wanted %s)", goruntime.GOOS, goruntime.GOARCH, assetName)}
	}

	exe, err := os.Executable()
	if err != nil {
		return UpdateResult{Error: "cannot determine executable path"}
	}
	exe, _ = filepath.EvalSymlinks(exe)

	runtime.EventsEmit(a.ctx, "update:progress", "Downloading app update...")
	dlResp, err := http.Get(downloadURL) //nolint:gosec
	if err != nil {
		return UpdateResult{Error: fmt.Sprintf("download error: %v", err)}
	}
	defer dlResp.Body.Close()

	tmp, err := os.CreateTemp("", "monoagent-app-update-*")
	if err != nil {
		return UpdateResult{Error: fmt.Sprintf("temp file: %v", err)}
	}
	tmpPath := tmp.Name()
	if _, err := io.Copy(tmp, dlResp.Body); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return UpdateResult{Error: fmt.Sprintf("write download: %v", err)}
	}
	tmp.Close()

	runtime.EventsEmit(a.ctx, "update:progress", "Installing app update...")

	switch goruntime.GOOS {
	case "darwin":
		return a.updateAppMacOS(exe, tmpPath, release.TagName)
	case "windows":
		return a.updateAppWindows(exe, tmpPath, release.TagName)
	case "linux":
		return a.updateAppLinux(exe, tmpPath, release.TagName)
	default:
		os.Remove(tmpPath)
		return UpdateResult{Error: "app update not supported on " + goruntime.GOOS}
	}
}

func (a *App) updateAppMacOS(exe, zipPath, newVersion string) UpdateResult {
	// exe = /path/to/MonoAgent.app/Contents/MacOS/monoagent-ui
	// .app bundle is 3 levels up
	appBundle := filepath.Dir(filepath.Dir(filepath.Dir(exe)))

	extractDir, err := os.MkdirTemp("", "monoagent-app-extract-*")
	if err != nil {
		os.Remove(zipPath)
		return UpdateResult{Error: fmt.Sprintf("extract dir: %v", err)}
	}

	// Use system unzip to preserve macOS symlinks inside .app bundles
	if out, err := exec.Command("unzip", "-q", zipPath, "-d", extractDir).CombinedOutput(); err != nil {
		os.Remove(zipPath)
		os.RemoveAll(extractDir)
		return UpdateResult{Error: fmt.Sprintf("unzip: %v — %s", err, out)}
	}
	os.Remove(zipPath)

	newApp := filepath.Join(extractDir, "MonoAgent.app")
	if _, err := os.Stat(newApp); err != nil {
		os.RemoveAll(extractDir)
		return UpdateResult{Error: "MonoAgent.app not found in downloaded archive"}
	}

	scriptPath := filepath.Join(os.TempDir(), "monoagent-app-update.sh")
	script := fmt.Sprintf(`#!/bin/bash
sleep 2
rm -rf %q
cp -r %q %q
rm -rf %q
open %q
rm -f %q
`, appBundle, newApp, appBundle, extractDir, appBundle, scriptPath)

	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		os.RemoveAll(extractDir)
		return UpdateResult{Error: fmt.Sprintf("write script: %v", err)}
	}

	if err := exec.Command("bash", scriptPath).Start(); err != nil { //nolint:gosec
		os.Remove(scriptPath)
		os.RemoveAll(extractDir)
		return UpdateResult{Error: fmt.Sprintf("start update script: %v", err)}
	}

	go func() {
		time.Sleep(300 * time.Millisecond)
		runtime.Quit(a.ctx)
	}()

	return UpdateResult{Success: true, NewVersion: newVersion}
}

// updateAppWindows atomically replaces the running exe using a detached bat script.
// tmpPath is the newly downloaded exe (already in a temp location — no zip involved).
func (a *App) updateAppWindows(exe, tmpPath, newVersion string) UpdateResult {
	batPath := filepath.Join(os.TempDir(), "monoagent-update.bat")
	// Use %s (not %q) so Windows paths retain single backslashes; wrap in double-quotes for spaces.
	bat := fmt.Sprintf("@echo off\r\ntimeout /t 2 /nobreak > nul\r\nmove /Y \"%s\" \"%s\"\r\nstart \"\" \"%s\"\r\ndel /Q \"%s\"\r\n",
		tmpPath, exe, exe, batPath)

	if err := os.WriteFile(batPath, []byte(bat), 0755); err != nil {
		os.Remove(tmpPath)
		return UpdateResult{Error: fmt.Sprintf("write bat: %v", err)}
	}

	if err := exec.Command("cmd.exe", "/C", batPath).Start(); err != nil { //nolint:gosec
		os.Remove(tmpPath)
		os.Remove(batPath)
		return UpdateResult{Error: fmt.Sprintf("start update script: %v", err)}
	}

	go func() {
		time.Sleep(300 * time.Millisecond)
		runtime.Quit(a.ctx)
	}()

	return UpdateResult{Success: true, NewVersion: newVersion}
}

func (a *App) updateAppLinux(exe, tarPath, newVersion string) UpdateResult {
	extractDir, err := os.MkdirTemp("", "monoagent-app-extract-*")
	if err != nil {
		os.Remove(tarPath)
		return UpdateResult{Error: fmt.Sprintf("extract dir: %v", err)}
	}

	if out, err := exec.Command("tar", "xzf", tarPath, "-C", extractDir).CombinedOutput(); err != nil {
		os.Remove(tarPath)
		os.RemoveAll(extractDir)
		return UpdateResult{Error: fmt.Sprintf("tar: %v — %s", err, out)}
	}
	os.Remove(tarPath)

	newBin := filepath.Join(extractDir, "MonoAgent-linux-amd64")
	if _, err := os.Stat(newBin); err != nil {
		os.RemoveAll(extractDir)
		return UpdateResult{Error: "MonoAgent-linux-amd64 not found in downloaded archive"}
	}

	if err := os.Chmod(newBin, 0755); err != nil {
		os.RemoveAll(extractDir)
		return UpdateResult{Error: fmt.Sprintf("chmod: %v", err)}
	}

	bak := exe + ".bak"
	os.Remove(bak)
	if err := os.Rename(exe, bak); err != nil {
		os.RemoveAll(extractDir)
		return UpdateResult{Error: fmt.Sprintf("backup: %v", err)}
	}
	if err := os.Rename(newBin, exe); err != nil {
		os.Rename(bak, exe)
		os.RemoveAll(extractDir)
		return UpdateResult{Error: fmt.Sprintf("install: %v", err)}
	}
	os.Remove(bak)
	os.RemoveAll(extractDir)

	go func() {
		time.Sleep(300 * time.Millisecond)
		runtime.Quit(a.ctx)
	}()

	return UpdateResult{Success: true, NewVersion: newVersion}
}

// appAssetName returns the GitHub release asset name for the Wails desktop app.
func appAssetName() string {
	switch goruntime.GOOS {
	case "darwin":
		if goruntime.GOARCH == "arm64" {
			return "MonoAgent-darwin-arm64.zip"
		}
		return "MonoAgent-darwin-amd64.zip"
	case "windows":
		return "MonoAgent-windows-amd64.exe"
	case "linux":
		return "MonoAgent-linux-amd64.tar.gz"
	default:
		return ""
	}
}

