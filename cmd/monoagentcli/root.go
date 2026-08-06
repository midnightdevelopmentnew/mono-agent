package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"monoagent/internal/connections"
	"monoagent/internal/secrets"
	"monoagent/internal/storage"
	"github.com/spf13/cobra"
)

type globalConfig struct {
	DBPath     string
	OutputDir  string
	ConfigDir  string
	Headless   bool
	Workers    int
	Verbose    bool
	JSONOutput bool
	LogFile    string
	ProfileID  string // active profile; defaults to value stored in settings table
}

func newRootCmd() *cobra.Command {
	cfg := &globalConfig{}

	cmd := &cobra.Command{
		Use:   "monoagentcli",
		Short: "Multi-platform social media automation agent",
		Long: `Mono Agent — automate keyword search, profile discovery, bulk messaging, content publishing, and more across Instagram, LinkedIn, X, TikTok, Telegram, and Email.

START HERE — what can this already do?

  monoagentcli workflow search [query]      Everything runnable: bundled
                                            templates + saved workflows, each
                                            with the command that runs it
  monoagentcli workflow templates show <id> Inputs, nodes, and exact run command
  monoagentcli ref templates                Guide to the bundled templates

All state (workflows, logins, generated images) lives in ~/.monoagent/, so every
command works from any directory — there is nothing to set up per project. Add
--json to the discovery commands above for machine-readable output.

AI agents: run 'monoagentcli ref' for built-in, offline documentation
covering every command and workflow node type in depth — including
'monoagentcli ref connections' for the profile/OAuth/credential model
(read this before writing anything that touches --profile or
--credential) and 'monoagentcli ref examples' for common patterns. This
is the primary, most current source of truth for how the CLI is meant to
be used — prefer it over guessing from --help output alone.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			// Best-effort: install Claude Code skill on first run if Claude is detected.
			runClaudeFirstRunCheck()
		},
	}

	// Global flags
	cmd.PersistentFlags().StringVar(&cfg.DBPath, "db-path", "~/.monoagent/monoagent.db", "SQLite database path")
	cmd.PersistentFlags().StringVar(&cfg.OutputDir, "output-dir", "~/.monoagent/output", "JSON file output directory")
	cmd.PersistentFlags().StringVar(&cfg.ConfigDir, "config-dir", "~/.monoagent/configs", "XPath config cache directory")
	cmd.PersistentFlags().BoolVar(&cfg.Headless, "headless", false, "Run browser in headless mode")
	cmd.PersistentFlags().IntVar(&cfg.Workers, "workers", 1, "Number of concurrent browser workers")
	cmd.PersistentFlags().BoolVarP(&cfg.Verbose, "verbose", "v", false, "Enable debug logging")
	cmd.PersistentFlags().BoolVar(&cfg.JSONOutput, "json", false, "Output in JSON format")
	cmd.PersistentFlags().StringVar(&cfg.LogFile, "log-file", "", "Path to log file")
	cmd.PersistentFlags().StringVar(&cfg.ProfileID, "profile", "", "Profile to use (defaults to the active profile in settings)")

	// Register subcommands
	cmd.AddCommand(
		newLoginCmd(cfg),
		newLogoutCmd(cfg),
		newRunCmd(cfg),
		newSearchCmd(cfg),
		newMessageCmd(cfg),
		newCommentCmd(cfg),
		newActionCmd(cfg),
		newCrawlCmd(cfg),
		newInitCmd(),
		newPeopleCmd(cfg),
		newListCmd(cfg),
		newTemplateCmd(cfg),
		newConfigCmd(cfg),
		newScheduleCmd(cfg),
		newExportCmd(cfg),
		newStatusCmd(cfg),
		newVersionCmd(),
		newUpdateCmd(),
		newWorkflowCmd(cfg),
		newDaemonCmd(cfg),
		newNodeCmd(cfg),
		newConnectCmd(cfg),
		newRefCmd(),
		newProfileCmd(cfg),
		newSecretCmd(cfg),
		newHILCmd(cfg),
		newAICmd(cfg),
	)

	return cmd
}

// expandPath expands ~ to the user's home directory.
func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

// initDB creates and returns a database connection, applying migrations.
// It also resolves cfg.ProfileID: if not set via --profile, reads it from the settings table.
func initDB(cfg *globalConfig) (*storage.Database, error) {
	dbPath := expandPath(cfg.DBPath)
	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		return nil, err
	}
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		return nil, err
	}
	if err := db.ApplyMigrations(); err != nil {
		db.Close()
		return nil, err
	}
	// Automatic, idempotent check-and-migrate: encrypt any connections rows
	// left over from before the secrets vault shipped. Cheap (a single COUNT
	// query) once everything is already encrypted, and self-healing if a
	// plaintext row is ever reintroduced. Non-fatal — a failure here must not
	// block the CLI from starting.
	if _, _, err := connections.EncryptPlaintextConnections(context.Background(), db.DB); err != nil {
		fmt.Fprintf(os.Stderr, "warning: connections migration: %v\n", err)
	}
	if _, _, err := secrets.MigrateFieldsToKV(context.Background(), db.DB); err != nil {
		fmt.Fprintf(os.Stderr, "warning: vault key-value migration: %v\n", err)
	}
	// Resolve active profile if not overridden on the command line.
	if cfg.ProfileID == "" {
		var id string
		_ = db.DB.QueryRow(`SELECT value FROM settings WHERE key = 'active_profile_id'`).Scan(&id)
		if id == "" {
			id = "default"
		}
		cfg.ProfileID = id
	} else {
		// --profile accepts either a profile's ID or its display name; resolve
		// to the canonical ID. Without this, --profile <name> silently scoped
		// every read/write to a profile_id string that matched no real
		// profile (data landed in "default" instead), since a name and its
		// ID only coincide for the "default" profile.
		resolved, err := resolveProfileID(db.DB, cfg.ProfileID)
		if err != nil {
			db.Close()
			return nil, err
		}
		cfg.ProfileID = resolved
	}
	return db, nil
}

// resolveProfileID accepts either a profile's ID or its name and returns the
// canonical ID, erroring if neither matches any row in `profiles`.
func resolveProfileID(db *sql.DB, idOrName string) (string, error) {
	var id string
	if err := db.QueryRow(`SELECT id FROM profiles WHERE id = ?`, idOrName).Scan(&id); err == nil {
		return id, nil
	}
	if err := db.QueryRow(`SELECT id FROM profiles WHERE name = ?`, idOrName).Scan(&id); err == nil {
		return id, nil
	}
	return "", fmt.Errorf("profile %q not found (checked both id and name)", idOrName)
}

// ensureDir creates a directory if it does not exist.
func ensureDir(path string) error {
	expanded := expandPath(path)
	return os.MkdirAll(expanded, 0755)
}
