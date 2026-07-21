package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	"monoagent/internal/bot"
	"monoagent/internal/chromecookies"
	"monoagent/internal/secrets"
	"monoagent/internal/storage"

	// Import platform bots to trigger init() registration.
	_ "monoagent/internal/bot/email"
	_ "monoagent/internal/bot/hackernews"
	_ "monoagent/internal/bot/instagram"
	_ "monoagent/internal/bot/linkedin"
	_ "monoagent/internal/bot/producthunt"
	_ "monoagent/internal/bot/telegram"
	_ "monoagent/internal/bot/tiktok"
	_ "monoagent/internal/bot/x"
)

// newExtensionLoginLogger builds the same warn-level stderr logger used by
// `node run`/`workflow run` when talking to the Chrome extension bridge.
func newExtensionLoginLogger() zerolog.Logger {
	return zerolog.New(os.Stderr).With().Timestamp().Str("component", "extension").Logger().Level(zerolog.WarnLevel)
}

// connChecker is the minimal slice of browser.ExtensionBridge that
// ensureExtensionConnected needs — defined locally to avoid pulling in the
// browser package just for a type name.
type connChecker interface {
	IsConnected() bool
}

// ensureExtensionConnected returns once the extension bridge is connected,
// launching the user's real Chrome first if it doesn't seem to be running.
// The launch is a bare process start — no --user-data-dir override, no
// --remote-debugging-port — so it opens (or focuses) the user's actual
// default profile, complete with whatever extensions (including this one)
// and logins are already there, rather than a blank throwaway profile.
func ensureExtensionConnected(bridge connChecker, timeout time.Duration) error {
	if bridge.IsConnected() {
		return nil
	}

	chromePath := findLocalChromePath()
	if chromePath == "" {
		return fmt.Errorf("Chrome extension not connected, and Google Chrome wasn't found on this machine — install Chrome and the mono-agent extension, then try again")
	}
	fmt.Fprintln(os.Stderr, "Chrome doesn't seem to be running — launching it now...")
	if err := exec.Command(chromePath).Start(); err != nil {
		return fmt.Errorf("launching Chrome: %w", err)
	}

	deadline := time.Now().Add(timeout)
	for !bridge.IsConnected() && time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
	}
	if !bridge.IsConnected() {
		return fmt.Errorf("Chrome opened, but the mono-agent extension didn't connect within %s — make sure it's installed and enabled, then try again", timeout)
	}
	return nil
}

// loginTabStatePath returns the path to the small state file recording the
// Chrome tab ID opened by `login <platform>`, so `login confirm <platform>`
// — a separate process invocation — can find that same tab in the user's
// real, extension-connected browser.
func loginTabStatePath(profileID, platform string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".monoagent", "login-tab-"+profileID+"-"+strings.ToLower(platform)+".json"), nil
}

func saveLoginTabID(profileID, platform string, tabID int) error {
	path, err := loginTabStatePath(profileID, platform)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(struct {
		TabID int `json:"tab_id"`
	}{TabID: tabID})
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func readLoginTabID(profileID, platform string) (int, error) {
	path, err := loginTabStatePath(profileID, platform)
	if err != nil {
		return 0, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("no login tab recorded — run `login %s` first: %w", strings.ToLower(platform), err)
	}
	var v struct {
		TabID int `json:"tab_id"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return 0, err
	}
	return v.TabID, nil
}

// convertExtensionCookies converts the raw chrome.cookies.getAll() result
// returned by the extension bridge (fields: name, value, domain, path,
// secure, httpOnly, expirationDate, ...) into chromecookies.Cookie — the
// shape cliSessionProvider.GetPage already expects when restoring a saved
// session (it json.Unmarshals cookies_json into []*proto.NetworkCookieParam,
// which uses these same field names).
func convertExtensionCookies(raw interface{}) ([]chromecookies.Cookie, error) {
	if raw == nil {
		return nil, nil
	}
	items, ok := raw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected cookies payload type %T", raw)
	}
	cookies := make([]chromecookies.Cookie, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		c := chromecookies.Cookie{
			Name:     stringField(m, "name"),
			Value:    stringField(m, "value"),
			Domain:   stringField(m, "domain"),
			Path:     stringField(m, "path"),
			Secure:   boolField(m, "secure"),
			HTTPOnly: boolField(m, "httpOnly"),
		}
		if exp, ok := m["expirationDate"].(float64); ok {
			c.Expires = exp
		}
		cookies = append(cookies, c)
	}
	return cookies, nil
}

func stringField(m map[string]interface{}, key string) string {
	v, _ := m[key].(string)
	return v
}

func boolField(m map[string]interface{}, key string) bool {
	v, _ := m[key].(bool)
	return v
}

// saveSession upserts a crawler_sessions row scoped to the active profile.
// UPDATE-then-INSERT (rather than INSERT OR REPLACE) because REPLACE keys
// only on UNIQUE(username, platform, profile_id) — a plain REPLACE would
// still be scoped correctly here, but going through UPDATE first avoids
// resetting the auto-increment id/when_added on every re-login.
func saveSession(db *storage.Database, profileID, platform, username string, cookiesJSON []byte) error {
	expiry := time.Now().Add(30 * 24 * time.Hour) // 30 days
	// Session cookies are live-login material; encrypt them under the same vault
	// envelope used for connection credentials rather than storing plaintext.
	enc, err := secrets.EncryptBlob(context.Background(), db.DB, cookiesJSON)
	if err != nil {
		return fmt.Errorf("encrypting session cookies: %w", err)
	}
	res, err := db.DB.Exec(
		`UPDATE crawler_sessions SET cookies_json = ?, expiry = ?
		 WHERE username = ? AND platform = ? AND profile_id = ?`,
		enc, expiry, username, platform, profileID,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_, err = db.DB.Exec(
			`INSERT INTO crawler_sessions (username, platform, cookies_json, expiry, profile_id)
			 VALUES (?, ?, ?, ?, ?)`,
			username, platform, enc, expiry, profileID,
		)
	}
	return err
}

func newLoginCmd(cfg *globalConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "login <platform>",
		Short: "Open a tab in your real Chrome to log in to a social platform",
		Long: "Opens the platform's login page as a new tab in your actual, already-running Chrome — via the " +
			"mono-agent Chrome extension — instead of a separate throwaway browser instance. Log in by hand (any " +
			"method, including Google/SSO buttons and bot-verification challenges), then run `login confirm " +
			"<platform>` to capture the session directly from that tab.",
		Example: `  monoagentcli login instagram
  monoagentcli login producthunt
  monoagentcli login confirm producthunt`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			platform := strings.ToUpper(args[0])

			factory, ok := bot.PlatformRegistry[platform]
			if !ok {
				supported := make([]string, 0, len(bot.PlatformRegistry))
				for k := range bot.PlatformRegistry {
					supported = append(supported, strings.ToLower(k))
				}
				return fmt.Errorf("unsupported platform %q; supported: %s", args[0], strings.Join(supported, ", "))
			}

			adapter := factory()

			// initDB's return value isn't needed here, but calling it is what
			// resolves cfg.ProfileID from whatever --profile was given (a
			// name or an ID) to the canonical ID — the same resolution
			// `login confirm` relies on via its own initDB call. Skipping
			// this would leave cfg.ProfileID as the raw --profile string, so
			// the two steps would compute different login-tab state files.
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			db.Close()

			bridge := setupExtensionBridge(newExtensionLoginLogger(), 3*time.Second)
			if err := ensureExtensionConnected(bridge, 30*time.Second); err != nil {
				return err
			}

			tabID, err := bridge.CreateTab(adapter.LoginURL())
			if err != nil {
				return fmt.Errorf("opening login tab: %w", err)
			}
			if err := saveLoginTabID(cfg.ProfileID, platform, tabID); err != nil {
				return fmt.Errorf("recording login tab: %w", err)
			}

			fmt.Fprintf(os.Stderr, "Opened %s login in your Chrome — please log in manually in that tab.\n", platform)
			fmt.Fprintf(os.Stderr, "Once logged in, run: monoagentcli login confirm %s\n", strings.ToLower(platform))
			return nil
		},
	}

	// Subcommand: login confirm <platform>
	cmd.AddCommand(newLoginConfirmCmd(cfg))

	// Subcommand: login status
	cmd.AddCommand(newLoginStatusCmd(cfg))

	return cmd
}

func newLoginConfirmCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "confirm <platform>",
		Short: "Capture the session after you've logged in via `login <platform>`",
		Long: "Reads the cookies directly from the Chrome tab opened by `login <platform>`, via the mono-agent " +
			"extension. Run this only after you've actually finished logging in (and any bot-verification " +
			"challenge) by hand in that tab.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			platformArg := args[0]
			platform := strings.ToUpper(platformArg)

			if _, ok := bot.PlatformRegistry[platform]; !ok {
				return fmt.Errorf("unsupported platform %q", platformArg)
			}

			// initDB must run first: it's what resolves cfg.ProfileID from
			// the raw --profile value to the canonical ID `login <platform>`
			// used to record the login-tab state file.
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			tabID, err := readLoginTabID(cfg.ProfileID, platform)
			if err != nil {
				return err
			}

			bridge := setupExtensionBridge(newExtensionLoginLogger(), 3*time.Second)
			if err := ensureExtensionConnected(bridge, 30*time.Second); err != nil {
				return err
			}

			rawCookies, err := bridge.NewPage(tabID).GetCookies()
			if err != nil {
				return fmt.Errorf("reading cookies from login tab: %w", err)
			}
			cookies, err := convertExtensionCookies(rawCookies)
			if err != nil {
				return fmt.Errorf("parsing cookies: %w", err)
			}
			if len(cookies) == 0 {
				return fmt.Errorf("no cookies found in the login tab — make sure you finished logging in before running this")
			}
			cookiesJSON, err := json.Marshal(cookies)
			if err != nil {
				return fmt.Errorf("marshalling cookies: %w", err)
			}

			// Without DOM access there's no reliable way to read the actual
			// username here — "unknown" matches the existing fallback these
			// bots already use when ExtractUsername can't determine one.
			username := "unknown"

			if err := saveSession(db, cfg.ProfileID, strings.ToLower(platform), username, cookiesJSON); err != nil {
				return fmt.Errorf("saving session: %w", err)
			}

			fmt.Fprintf(os.Stderr, "Captured %d cookie(s) for %s (user: %s). Session saved.\n", len(cookies), platform, username)
			fmt.Printf("username: %s\n", username)
			return nil
		},
	}
}

func newLoginStatusCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show login status for all platforms",
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			rows, err := db.DB.Query(
				`SELECT id, username, platform, expiry, when_added FROM crawler_sessions WHERE profile_id = ? ORDER BY platform`,
				cfg.ProfileID,
			)
			if err != nil {
				return fmt.Errorf("querying sessions: %w", err)
			}
			defer rows.Close()

			type sessionRow struct {
				ID        int
				Username  string
				Platform  string
				Expiry    time.Time
				WhenAdded time.Time
			}

			var sessions []sessionRow
			for rows.Next() {
				var s sessionRow
				if err := rows.Scan(&s.ID, &s.Username, &s.Platform, &s.Expiry, &s.WhenAdded); err != nil {
					return fmt.Errorf("scanning session row: %w", err)
				}
				sessions = append(sessions, s)
			}
			if err := rows.Err(); err != nil {
				return fmt.Errorf("iterating sessions: %w", err)
			}

			if cfg.JSONOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(sessions)
			}

			if len(sessions) == 0 {
				fmt.Println("No active sessions found.")
				return nil
			}

			table := tablewriter.NewWriter(os.Stdout)
			table.SetHeader([]string{"ID", "Platform", "Username", "Status", "Expires", "Added"})
			table.SetBorder(false)
			table.SetAutoWrapText(false)

			now := time.Now()
			for _, s := range sessions {
				status := "active"
				if s.Expiry.Before(now) {
					status = "expired"
				}
				table.Append([]string{
					fmt.Sprintf("%d", s.ID),
					s.Platform,
					s.Username,
					status,
					s.Expiry.Format("2006-01-02 15:04"),
					s.WhenAdded.Format("2006-01-02 15:04"),
				})
			}
			table.Render()
			return nil
		},
	}
}

func newLogoutCmd(cfg *globalConfig) *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:   "logout [platform]",
		Short: "Delete saved session for a platform",
		Long:  "Removes saved cookies/session for the specified platform. Use --all to remove all sessions.",
		Example: `  monoagentcli logout instagram
  monoagentcli logout --all`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			if all {
				result, err := db.DB.Exec("DELETE FROM crawler_sessions WHERE profile_id = ?", cfg.ProfileID)
				if err != nil {
					return fmt.Errorf("deleting all sessions: %w", err)
				}
				count, _ := result.RowsAffected()
				fmt.Fprintf(os.Stderr, "Deleted %d session(s).\n", count)
				return nil
			}

			if len(args) == 0 {
				return fmt.Errorf("specify a platform or use --all")
			}

			platform := strings.ToLower(args[0])
			result, err := db.DB.Exec("DELETE FROM crawler_sessions WHERE platform = ? AND profile_id = ?", platform, cfg.ProfileID)
			if err != nil {
				return fmt.Errorf("deleting session for %s: %w", platform, err)
			}
			count, _ := result.RowsAffected()
			if count == 0 {
				fmt.Fprintf(os.Stderr, "No session found for %s.\n", platform)
			} else {
				fmt.Fprintf(os.Stderr, "Deleted session for %s.\n", platform)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "Delete all sessions")

	return cmd
}
