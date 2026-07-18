package main

import (
	"embed"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

// Version is injected at build time via ldflags.
// Falls back to git describe at runtime when built without ldflags (e.g. wails dev).
var (
	version   = ""
	buildDate = ""
)

func init() {
	if version == "" {
		if out, err := exec.Command("git", "describe", "--tags", "--always").Output(); err == nil {
			version = strings.TrimSpace(string(out))
		} else {
			version = "dev"
		}
	}
	if buildDate == "" {
		buildDate = time.Now().UTC().Format("2006-01-02T15:04:05Z")
	}
}

// enableDevTools controls whether the WebKit inspector opens on startup.
// Set to true temporarily while debugging frontend issues.
const enableDevTools = false

//go:embed all:frontend/dist
var assets embed.FS

// vaultImageHandler serves files from ~/.monoagent/vault/ at /vault-image/<filename>,
// scoped to the currently active profile so one profile cannot enumerate or view
// another profile's vault images.
func vaultImageHandler(app *App) http.Handler {
	vaultDir := filepath.Join(os.Getenv("HOME"), ".monoagent", "vault")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/vault-image/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// Use filepath.Base to prevent path traversal.
		name := filepath.Base(r.URL.Path)

		if app.db == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		profileID := app.getActiveProfileID()
		if profileID == "" {
			profileID = "default"
		}
		var exists int
		err := app.db.QueryRow(
			`SELECT 1 FROM vault_images WHERE filename = ? AND profile_id = ?`,
			name, profileID,
		).Scan(&exists)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		http.ServeFile(w, r, filepath.Join(vaultDir, name))
	})
}

func main() {
	app := NewApp()

	err := wails.Run(&options.App{
		Title:            "Mono Agent",
		Width:            1440,
		Height:           900,
		MinWidth:         1100,
		MinHeight:        700,
		BackgroundColour: &options.RGBA{R: 4, G: 6, B: 10, A: 255},
		AssetServer: &assetserver.Options{
			Assets:  assets,
			Handler: vaultImageHandler(app),
		},
		OnStartup:  app.startup,
		OnShutdown: app.shutdown,
		Bind:       []interface{}{app},
		Debug: options.Debug{
			OpenInspectorOnStartup: enableDevTools,
		},
		Mac: &mac.Options{
			TitleBar: &mac.TitleBar{
				TitlebarAppearsTransparent: true,
				HideTitle:                  true,
				FullSizeContent:            true,
			},
			WebviewIsTransparent: true,
			WindowIsTranslucent:  true,
		},
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			DisableWindowIcon:    false,
		},
	})
	if err != nil {
		println("Error:", err.Error())
	}
}
