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

// HybridSessionProvider tries the Chrome extension first for browser pages,
// falling back to Rod when the extension is not connected.
type HybridSessionProvider struct {
	ExtBridge   ExtensionBridge // may be nil if extension not configured
	RodProvider SessionProvider // fallback Rod-based provider
	Logger      zerolog.Logger
}

func (h *HybridSessionProvider) GetPage(ctx context.Context, platform, username string) (PageInterface, error) {
	connected := h.ExtBridge != nil && h.ExtBridge.IsConnected()
	h.Logger.Info().Bool("ext_connected", connected).Str("platform", platform).Msg("GetPage called")

	if connected {
		platformURLs := map[string]string{
			"gemini":    "https://gemini.google.com/app",
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
		if err == nil {
			h.Logger.Info().Str("platform", platform).Int("tabId", tabID).Msg("using Chrome extension")
			return h.ExtBridge.NewPage(tabID), nil
		}
		h.Logger.Warn().Err(err).Msg("extension tab creation failed, falling back to Rod")
	}

	if h.RodProvider != nil {
		return h.RodProvider.GetPage(ctx, platform, username)
	}
	return nil, fmt.Errorf("no browser provider available (extension not connected, no Rod fallback)")
}

// Close shuts down the Rod fallback provider if it exposes a Close method
// (e.g. cliSessionProvider quitting its launched Chromium process). Extension
// tabs are not tracked here — each one is closed by the node that opened it
// (see nodes.BrowserNode.Execute) right after use, since GetPage always opens
// a fresh tab rather than reusing one. Safe to call even if GetPage was never
// invoked.
func (h *HybridSessionProvider) Close() {
	if closer, ok := h.RodProvider.(interface{ Close() }); ok {
		closer.Close()
	}
}
