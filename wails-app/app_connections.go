package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/google/uuid"
	"monoagent/internal/connections"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)


// ─────────────────────────────────────────────────────────────────────────────
// Connections
// ─────────────────────────────────────────────────────────────────────────────

// CredentialOption is a lightweight connection summary used to populate
// credential dropdowns in the workflow node inspector.
type CredentialOption struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Platform string `json:"platform"`
	Method   string `json:"method"`
}

// ListCredentialsForNode returns credential options relevant to a given node type.
// Social platform nodes (action.instagram.*, action.linkedin.*, etc.) get browser
// method credentials; API service nodes get their matching platform's connections.
func (a *App) ListCredentialsForNode(nodeType string) []CredentialOption {
	if a.db == nil {
		return []CredentialOption{}
	}
	store := connections.NewStore(a.db)
	var platform string

	// Detect social platform from node type prefix (e.g. "action.instagram.publish_post")
	socialPlatforms := []string{"instagram", "linkedin", "tiktok", "x", "twitter"}
	lnodeType := strings.ToLower(nodeType)
	for _, sp := range socialPlatforms {
		if strings.Contains(lnodeType, sp) {
			platform = sp
			break
		}
	}

	// Service nodes: map by known service identifiers
	if platform == "" {
		serviceMap := map[string]string{
			"openrouter":    "openrouter",
			"huggingface":   "huggingface",
			"google_sheets": "google",
			"gmail":         "google",
			"slack":         "slack",
		}
		for key, pid := range serviceMap {
			if strings.Contains(lnodeType, key) {
				platform = pid
				break
			}
		}
	}

	var conns []connections.Connection
	var err error
	if platform != "" {
		conns, err = store.ListByPlatform(a.ctx, platform, a.getActiveProfileID())
	} else {
		conns, err = store.ListAll(a.ctx, a.getActiveProfileID())
	}
	if err != nil || conns == nil {
		return []CredentialOption{}
	}
	opts := make([]CredentialOption, 0, len(conns))
	for _, c := range conns {
		opts = append(opts, CredentialOption{
			ID:       c.ID,
			Label:    c.Label,
			Platform: c.Platform,
			Method:   string(c.Method),
		})
	}
	return opts
}

// ListConnections returns all saved connections for the active profile, filtered by platform if non-empty.
// Credential material (Connection.Data) is stripped before crossing the Wails IPC boundary.
func (a *App) ListConnections(platform string) []connections.SafeConnection {
	if a.connMgr == nil {
		return []connections.SafeConnection{}
	}
	result, err := a.connMgr.List(a.ctx, platform, a.getActiveProfileID())
	if err != nil {
		return []connections.SafeConnection{}
	}
	return connections.RedactAll(result)
}

// PlatformInfo is a frontend-safe representation of a platform (no OAuth secrets).
type PlatformInfo struct {
	ID         string                                       `json:"id"`
	Name       string                                       `json:"name"`
	Category   string                                       `json:"category"`
	ConnectVia string                                       `json:"connectVia"`
	Methods    []string                                     `json:"methods"`
	Fields     map[string][]connections.CredentialField     `json:"fields"`
	IconEmoji  string                                       `json:"iconEmoji"`
}

func toPlatformInfo(p connections.PlatformDef) PlatformInfo {
	methods := make([]string, len(p.Methods))
	for i, m := range p.Methods {
		methods[i] = string(m)
	}
	fields := make(map[string][]connections.CredentialField)
	for method, cfields := range p.Fields {
		fields[string(method)] = cfields
	}
	return PlatformInfo{
		ID:         p.ID,
		Name:       p.Name,
		Category:   p.Category,
		ConnectVia: p.ConnectVia,
		Methods:    methods,
		Fields:     fields,
		IconEmoji:  p.IconEmoji,
	}
}

// ListPlatformsJSON returns all platforms as a JSON string (bypasses Wails type serialization).
func (a *App) ListPlatformsJSON(connectVia string) string {
	var platforms []connections.PlatformDef
	if connectVia == "" {
		platforms = connections.All()
	} else {
		platforms = connections.ByConnectVia(connectVia)
	}
	result := make([]PlatformInfo, len(platforms))
	for i, p := range platforms {
		result[i] = toPlatformInfo(p)
	}
	b, err := json.Marshal(result)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// TestConnection re-validates a connection by ID.
// For OAuth connections it attempts a silent token refresh first.
// For browser sessions (social platforms), it checks session expiry and cookie presence.
func (a *App) TestConnection(id string) string {
	if a.connMgr == nil && a.db == nil {
		return "error: manager not initialized"
	}

	// First try the connections table (OAuth/API key connections).
	if a.connMgr != nil {
		if conn, err := a.connMgr.Get(a.ctx, id); err == nil && conn != nil {
			// Enforce profile scope.
			if conn.ProfileID != "" && conn.ProfileID != a.getActiveProfileID() {
				return "error: connection not found"
			}
			// OAuth: attempt silent token refresh before testing.
			if conn.Method == "oauth" {
				if _, refreshErr := a.getResourceCredentialData(a.ctx, id); refreshErr != nil {
					fmt.Printf("token refresh attempted: %v\n", refreshErr)
				}
			}
			if err := a.connMgr.Test(a.ctx, id); err != nil {
				return fmt.Sprintf("error: %v", err)
			}
			return "ok"
		}
	}

	// Fallback: check crawler_sessions (browser sessions for social platforms).
	// The UI passes integer session IDs for social platforms.
	if a.db != nil {
		var platform, cookiesJSON, expiry string
		err := a.db.QueryRow(
			`SELECT platform, cookies_json, expiry FROM crawler_sessions WHERE id = ? AND profile_id = ?`, id, a.getActiveProfileID(),
		).Scan(&platform, &cookiesJSON, &expiry)
		if err == nil {
			// Check expiry
			if exp, pErr := time.Parse("2006-01-02 15:04:05", expiry); pErr == nil {
				if time.Now().After(exp) {
					return "error: session expired — please log in again via the browser"
				}
			} else if exp2, pErr2 := time.Parse(time.RFC3339, expiry); pErr2 == nil {
				if time.Now().After(exp2) {
					return "error: session expired — please log in again via the browser"
				}
			}
			// Check cookies present
			if cookiesJSON == "" || cookiesJSON == "[]" || cookiesJSON == "null" {
				return "error: no session cookies stored — please log in again"
			}
			return "ok"
		}
	}

	// Also try looking up by platform name (fallback for credential_id = platform string).
	if a.connMgr != nil {
		platform := nodeTypeToPlatform(id)
		if conns, err := a.connMgr.List(a.ctx, platform, a.getActiveProfileID()); err == nil && len(conns) > 0 {
			for _, c := range conns {
				if c.Status == "active" {
					return "ok"
				}
			}
		}
	}

	return "error: connection not found"
}

// RemoveConnection deletes a connection by ID, scoped to the active profile.
func (a *App) RemoveConnection(id string) string {
	if a.connMgr == nil {
		return "error: manager not initialized"
	}
	// Verify ownership before deletion.
	conn, err := a.connMgr.Get(a.ctx, id)
	if err != nil || conn == nil {
		return "error: connection not found"
	}
	if conn.ProfileID != "" && conn.ProfileID != a.getActiveProfileID() {
		return "error: connection not found"
	}
	if err := a.connMgr.Remove(a.ctx, id, a.getActiveProfileID()); err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	return "ok"
}

// GetConnectionsForPlatform returns connections filtered by platform ID.
// Credential material (Connection.Data) is stripped before crossing the Wails IPC boundary.
func (a *App) GetConnectionsForPlatform(platformID string) []connections.SafeConnection {
	return a.ListConnections(platformID)
}

// GetOAuthCredentials returns the stored OAuth client_id and client_secret for
// a platform, scoped to the active profile, as JSON. Returns JSON
// {"clientID":"...","clientSecret":"..."} or "" if not set. Credentials are
// per-profile because two connections for the same platform under different
// profiles may need different Azure/OAuth app registrations.
func (a *App) GetOAuthCredentials(platformID string) string {
	if a.db == nil {
		return ""
	}
	// Route through the connections store so the encrypted client_secret is
	// decrypted (the store owns the vault envelope; reading the column raw here
	// would return a "vaultenc:v1:..." blob).
	clientID, clientSecret := connections.NewStore(a.db).GetOAuthClient(a.ctx, platformID, a.getActiveProfileID())
	if clientID == "" && clientSecret == "" {
		return ""
	}
	b, _ := json.Marshal(map[string]string{"clientID": clientID, "clientSecret": clientSecret})
	return string(b)
}

// SetOAuthCredentials saves OAuth client_id and client_secret for a platform,
// scoped to the active profile. clientSecret may be empty for public-client
// OAuth apps (e.g. those using PKCE, like a desktop app registration with no
// client secret).
func (a *App) SetOAuthCredentials(platformID, clientID, clientSecret string) string {
	if a.db == nil {
		return "error: db not available"
	}
	if clientID == "" {
		return "error: clientID is required"
	}
	// Route through the store so client_secret is encrypted under the vault
	// envelope (matching the reader and the auto-persist path).
	if err := connections.NewStore(a.db).SaveOAuthClient(a.ctx, platformID, a.getActiveProfileID(), clientID, clientSecret); err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	return "ok"
}

// ConnectPlatformOAuth starts an OAuth flow in a background goroutine.
// Emits "conn:progress" events with {platform, message, kind} and a final
// "conn:done" event with {platform, success, accountID?, error?}.
// Returns "started" immediately, or "error: ..." if preconditions fail.
func (a *App) ConnectPlatformOAuth(platformID string) string {
	if a.connMgr == nil {
		return "error: manager not initialized"
	}
	p, ok := connections.Get(platformID)
	if !ok {
		return fmt.Sprintf("error: unknown platform %q", platformID)
	}
	if p.OAuth == nil {
		return "error: platform does not support OAuth"
	}

	go func() {
		emit := func(msg, kind string) {
			runtime.EventsEmit(a.ctx, "conn:progress", map[string]interface{}{
				"platform": platformID,
				"message":  msg,
				"kind":     kind,
			})
		}

		// Resolve DB-stored OAuth credentials and pass them directly.
		var oauthClientID, oauthClientSecret string
		if credsJSON := a.GetOAuthCredentials(platformID); credsJSON != "" {
			var creds map[string]string
			if json.Unmarshal([]byte(credsJSON), &creds) == nil {
				oauthClientID = creds["clientID"]
				oauthClientSecret = creds["clientSecret"]
			}
		}

		conn, err := a.connMgr.ConnectOAuthWithProgress(a.ctx, platformID, emit, oauthClientID, oauthClientSecret, a.getActiveProfileID())
		if err != nil {
			runtime.EventsEmit(a.ctx, "conn:done", map[string]interface{}{
				"platform": platformID,
				"success":  false,
				"error":    err.Error(),
			})
			return
		}

		runtime.EventsEmit(a.ctx, "conn:done", map[string]interface{}{
			"platform":  platformID,
			"success":   true,
			"accountID": conn.AccountID,
		})
	}()

	return "started"
}

// LoginSocial spawns `monoagentcli login <platform>` as a subprocess, which
// opens the login page as a new tab in the user's real, already-running
// Chrome (via the mono-agent extension) and returns immediately. Log in
// happens entirely by hand; the UI should then call ConfirmSocialLogin once
// the user says they're done, which is the only step that reads cookies
// back out of that tab. Splitting these apart (rather than one call that
// auto-polls until login completes) is what lets bot-verification
// challenges (Google sign-in, Cloudflare, etc.) succeed — continuous
// automated activity during the challenge is itself a signal those systems
// detect. Emits "conn:opened" (tab is up, waiting for the user) or
// "conn:done" with success:false on failure to open it.
func (a *App) LoginSocial(platform string) string {
	pid := strings.ToLower(platform)
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}

	emit := func(msg, kind string) {
		runtime.EventsEmit(a.ctx, "conn:progress", map[string]interface{}{
			"platform": pid,
			"message":  msg,
			"kind":     kind,
		})
	}

	go func() {
		cmd := exec.CommandContext(a.ctx, cliBin, "--profile", a.getActiveProfileID(), "login", pid)
		stderr, _ := cmd.StderrPipe()

		if startErr := cmd.Start(); startErr != nil {
			emit(fmt.Sprintf("Failed to start login: %v", startErr), "error")
			runtime.EventsEmit(a.ctx, "conn:done", map[string]interface{}{"platform": pid, "success": false, "error": startErr.Error()})
			return
		}

		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			emit(scanner.Text(), "info")
		}

		if waitErr := cmd.Wait(); waitErr != nil {
			runtime.EventsEmit(a.ctx, "conn:done", map[string]interface{}{"platform": pid, "success": false, "error": waitErr.Error()})
			return
		}
		runtime.EventsEmit(a.ctx, "conn:opened", map[string]interface{}{"platform": pid})
	}()

	return "started"
}

// ConfirmSocialLogin spawns `monoagentcli login confirm <platform>`, the
// one step that reconnects to the Chrome tab LoginSocial opened and reads
// its cookies to capture the session. Call this after the user has finished
// logging in (and any bot-verification challenge) by hand.
// Progress/result are streamed via the same "conn:progress"/"conn:done"
// events LoginSocial uses.
func (a *App) ConfirmSocialLogin(platform string) string {
	pid := strings.ToLower(platform)
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}

	emit := func(msg, kind string) {
		runtime.EventsEmit(a.ctx, "conn:progress", map[string]interface{}{
			"platform": pid,
			"message":  msg,
			"kind":     kind,
		})
	}

	go func() {
		cmd := exec.CommandContext(a.ctx, cliBin, "--profile", a.getActiveProfileID(), "login", "confirm", pid)
		stdout, _ := cmd.StdoutPipe()
		stderr, _ := cmd.StderrPipe()

		if startErr := cmd.Start(); startErr != nil {
			emit(fmt.Sprintf("Failed to start: %v", startErr), "error")
			runtime.EventsEmit(a.ctx, "conn:done", map[string]interface{}{"platform": pid, "success": false, "error": startErr.Error()})
			return
		}

		go func() {
			scanner := bufio.NewScanner(stderr)
			for scanner.Scan() {
				emit(scanner.Text(), "info")
			}
		}()

		scanner := bufio.NewScanner(stdout)
		var username string
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "username: ") {
				username = strings.TrimPrefix(line, "username: ")
			}
		}

		waitErr := cmd.Wait()
		if waitErr != nil {
			runtime.EventsEmit(a.ctx, "conn:done", map[string]interface{}{"platform": pid, "success": false, "error": waitErr.Error()})
			return
		}
		if username == "" {
			username = "unknown"
		}
		runtime.EventsEmit(a.ctx, "conn:done", map[string]interface{}{"platform": pid, "success": true, "accountID": username})
	}()

	return "started"
}

// SaveConnectionDirect saves a connection directly from the UI with provided field values.
// fieldValuesJSON is a JSON object string (avoids Wails map serialization issues).
// Returns "ok:<id>" on success or "error: ..." on failure.
func (a *App) SaveConnectionDirect(platformID string, method string, fieldValuesJSON string) string {
	if a.connMgr == nil {
		return "error: manager not initialized"
	}
	p, ok := connections.Get(platformID)
	if !ok {
		return fmt.Sprintf("error: unknown platform %q", platformID)
	}
	var fieldValues map[string]interface{}
	if err := json.Unmarshal([]byte(fieldValuesJSON), &fieldValues); err != nil {
		return fmt.Sprintf("error: invalid field values JSON: %v", err)
	}
	now := time.Now().Format(time.RFC3339)
	conn := &connections.Connection{
		ID:        uuid.New().String(),
		Platform:  platformID,
		Method:    connections.AuthMethod(method),
		Label:     p.Name,
		Data:      fieldValues,
		Status:    "active",
		ProfileID: a.getActiveProfileID(),
		CreatedAt: now,
		UpdatedAt: now,
	}
	// Validate the connection
	accountID, err := connections.ValidateConnection(a.ctx, conn)
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	if accountID != "" {
		conn.AccountID = accountID
		conn.Label = fmt.Sprintf("%s – %s", p.Name, accountID)
	}
	// Save to DB
	store := connections.NewStore(a.db)
	if err := store.EnsureTable(a.ctx); err != nil {
		return fmt.Sprintf("error: table init: %v", err)
	}
	if err := store.Save(a.ctx, conn); err != nil {
		return fmt.Sprintf("error: save: %v", err)
	}
	return "ok:" + conn.ID
}

