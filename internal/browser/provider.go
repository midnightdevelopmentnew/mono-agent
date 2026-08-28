package browser

import (
	"context"
	"fmt"
	"strings"

	"github.com/rs/zerolog"
)

// SessionProvider is the interface that entry points must satisfy.
// This matches the existing nodes.SessionProvider interface.
type SessionProvider interface {
	GetPage(ctx context.Context, platform string, username string) (PageInterface, error)
}

// ExtensionBridge abstracts the Chrome extension server so that the browser
// package does not import internal/extension directly (avoiding an import cycle).
// Callers pass in a concrete *extension.Server wrapped in a thin adapter.
type ExtensionBridge interface {
	IsConnected() bool
	CreateTab(url string) (int, error)
	CloseTab(tabID int) error
	NewPage(tabID int) PageInterface
}

// HybridSessionProvider gets browser pages exclusively through the Chrome
// extension bridge. There is no local browser fallback: if the extension
// isn't connected, GetPage fails instead of launching a browser process.
type HybridSessionProvider struct {
	ExtBridge ExtensionBridge // may be nil if extension not configured
	Logger    zerolog.Logger
}

func (h *HybridSessionProvider) GetPage(ctx context.Context, platform, username string) (PageInterface, error) {
	connected := h.ExtBridge != nil && h.ExtBridge.IsConnected()
	h.Logger.Info().Bool("ext_connected", connected).Str("platform", platform).Msg("GetPage called")

	if !connected {
		return nil, fmt.Errorf("Chrome extension not connected — no browser is launched as a fallback; connect the extension and try again")
	}

	platformURLs := map[string]string{
		"gemini":    "https://gemini.google.com/app",
		"chatgpt":   "https://chatgpt.com/",
		"instagram": "https://www.instagram.com",
		"linkedin":  "https://www.linkedin.com",
		"x":         "https://x.com",
		"tiktok":    "https://www.tiktok.com",
	}
	url := platformURLs[strings.ToLower(platform)]
	if url == "" {
		url = "about:blank"
	}
	tabID, err := h.ExtBridge.CreateTab(url)
	if err != nil {
		return nil, fmt.Errorf("creating extension tab: %w", err)
	}
	h.Logger.Info().Str("platform", platform).Int("tabId", tabID).Msg("using Chrome extension")
	return h.ExtBridge.NewPage(tabID), nil
}

// Close is a no-op now that there is no Rod fallback provider to shut down;
// kept so existing `defer hybridProvider.Close()` call sites don't need to change.
func (h *HybridSessionProvider) Close() {}
