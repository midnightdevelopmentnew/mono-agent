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

// findSystemChrome returns the path to the user's real Chrome/Chromium browser.
// Falls back to empty string (Rod default) if none found.
func findSystemChrome() string {
	paths := []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
		"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// loginProfileDir returns the Chrome user-data-dir for a given app-profile +
// platform pair. Scoped per-platform (not just per-profile) because Chrome
// enforces a single running instance per user-data-dir — sharing one dir
// across platforms meant opening a second platform's login while the first
// was still open silently handed off to the already-running instance.
func loginProfileDir(profileID, platform string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".monoagent", "chrome-profile-"+profileID+"-"+strings.ToLower(platform)), nil
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
		 WHERE username = ? AND platform = ? AND COALESCE(profile_id,'default') = ?`,
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
		Short: "Open a browser window to log in to a social platform",
		Long: "Opens a genuinely plain browser window at the platform's login page — no --remote-debugging-port, " +
			"no CDP, nothing at all distinguishing it from a browser you launched yourself — and returns immediately. " +
			"Log in by hand (any method, including Google/SSO buttons and bot-verification challenges), then run " +
			"`login confirm <platform>` to capture the session by reading it directly from the browser's own cookie " +
			"store on disk.",
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
			// the two steps would compute different chrome-profile-* dirs
			// and never find each other's data.
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			db.Close()

			chromePath := findSystemChrome()
			if chromePath == "" {
				return fmt.Errorf("no supported browser found (Chrome, Chromium, Brave, or Edge)")
			}
			userDataDir, err := loginProfileDir(cfg.ProfileID, platform)
			if err != nil {
				return fmt.Errorf("resolving profile directory: %w", err)
			}
			if err := os.MkdirAll(userDataDir, 0o755); err != nil {
				return fmt.Errorf("creating profile directory: %w", err)
			}

			// A raw process launch — no go-rod, no launcher, no
			// --remote-debugging-port at all. This is deliberate: sites like
			// Google's sign-in flow appear to detect the mere presence of a
			// remote debugging port, regardless of launch flags or whether
			// anything is actively connected to it. --no-first-run just
			// skips the onboarding dialog and carries no automation signal.
			// The session is captured afterward by `login confirm`, which
			// reads this profile's own Cookies database directly off disk
			// instead of via CDP.
			cmdChrome := exec.Command(chromePath, "--no-first-run", "--user-data-dir="+userDataDir, adapter.LoginURL())
			if err := cmdChrome.Start(); err != nil {
				return fmt.Errorf("launching browser: %w", err)
			}

			fmt.Fprintf(os.Stderr, "Browser opened — please log in to %s manually in the window that appeared.\n", platform)
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
		Long: "Reads and decrypts the cookies saved for that platform directly from the browser's own cookie " +
			"database on disk (via the macOS Keychain — you may be prompted to approve access the first time), " +
			"rather than connecting to the browser at all. Run this only after you've actually finished logging in " +
			"(and any bot-verification challenge) by hand.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			platformArg := args[0]
			platform := strings.ToUpper(platformArg)

			factory, ok := bot.PlatformRegistry[platform]
			if !ok {
				return fmt.Errorf("unsupported platform %q", platformArg)
			}
			adapter := factory()

			// initDB must run first: it's what resolves cfg.ProfileID from
			// the raw --profile value to the canonical ID `login <platform>`
			// used to pick this same profile directory.
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			chromePath := findSystemChrome()
			if chromePath == "" {
				return fmt.Errorf("no supported browser found (Chrome, Chromium, Brave, or Edge)")
			}
			userDataDir, err := loginProfileDir(cfg.ProfileID, platform)
			if err != nil {
				return fmt.Errorf("resolving profile directory: %w", err)
			}

			domain, err := chromecookies.DomainFromLoginURL(adapter.LoginURL())
			if err != nil {
				return fmt.Errorf("determining cookie domain: %w", err)
			}

			cookies, err := chromecookies.ReadCookies(chromePath, userDataDir, domain)
			if err != nil {
				return err
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
				`SELECT id, username, platform, expiry, when_added FROM crawler_sessions WHERE COALESCE(profile_id,'default') = ? ORDER BY platform`,
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
				result, err := db.DB.Exec("DELETE FROM crawler_sessions WHERE COALESCE(profile_id,'default') = ?", cfg.ProfileID)
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
			result, err := db.DB.Exec("DELETE FROM crawler_sessions WHERE platform = ? AND COALESCE(profile_id,'default') = ?", platform, cfg.ProfileID)
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
