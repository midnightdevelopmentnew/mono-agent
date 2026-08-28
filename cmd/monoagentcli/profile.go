package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

// printJSON writes v to stdout as indented JSON. Small shared helper for the
// commands that honor the global --json flag.
func printJSON(v interface{}) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func newProfileCmd(cfg *globalConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Manage profiles (sessions, credentials, and workflows are profile-scoped)",
	}
	cmd.AddCommand(
		newProfileListCmd(cfg),
		newProfileCreateCmd(cfg),
		newProfileSwitchCmd(cfg),
		newProfileCurrentCmd(cfg),
	)
	return cmd
}

func newProfileListCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all profiles",
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := initDB(cfg)
			if err != nil {
				return err
			}
			defer db.Close()

			// Resolve the active profile before opening the profiles cursor
			// below — running a second query while `rows` is still open (not
			// yet closed/drained) can hold the connection pool's only
			// connection when the pool is capped to one, deadlocking.
			var activeID string
			_ = db.DB.QueryRow(`SELECT value FROM settings WHERE key = 'active_profile_id'`).Scan(&activeID)

			rows, err := db.DB.Query(`SELECT id, name, created_at FROM profiles ORDER BY created_at ASC`)
			if err != nil {
				return fmt.Errorf("list profiles: %w", err)
			}
			defer rows.Close()

			type profileRow struct {
				ID        string `json:"id"`
				Name      string `json:"name"`
				CreatedAt string `json:"created_at"`
				Active    bool   `json:"active"`
				WSURL     string `json:"ws_url"`
			}
			var profiles []profileRow
			for rows.Next() {
				var p profileRow
				if rows.Scan(&p.ID, &p.Name, &p.CreatedAt) == nil {
					p.Active = p.ID == activeID
					p.WSURL = ExtensionWSURLForProfile(p.ID)
					profiles = append(profiles, p)
				}
			}
			if err := rows.Err(); err != nil {
				return err
			}

			if cfg.JSONOutput {
				if profiles == nil {
					profiles = []profileRow{}
				}
				return printJSON(profiles)
			}

			fmt.Printf("%-36s  %-20s  %-22s  %s\n", "ID", "NAME", "CREATED", "EXTENSION WS URL")
			for _, p := range profiles {
				marker := ""
				if p.Active {
					marker = " *"
				}
				fmt.Printf("%-36s  %-20s  %-22s  %s%s\n", p.ID, p.Name, p.CreatedAt, p.WSURL, marker)
			}
			fmt.Println()
			fmt.Println("Each profile drives its own browser on its own bridge port. Configure the")
			fmt.Println("mono-agent extension in that profile's Chrome with the WS URL shown above")
			fmt.Println("(extension popup -> WebSocket URL). Two browsers on one port evict each other.")
			return nil
		},
	}
}

func newProfileCreateCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := initDB(cfg)
			if err != nil {
				return err
			}
			defer db.Close()

			id := uuid.New().String()
			name := args[0]
			_, err = db.DB.Exec(`INSERT INTO profiles (id, name) VALUES (?, ?)`, id, name)
			if err != nil {
				return fmt.Errorf("create profile: %w", err)
			}
			if cfg.JSONOutput {
				return printJSON(map[string]string{"id": id, "name": name})
			}
			fmt.Printf("Created profile: %s (%s)\n", name, id)
			return nil
		},
	}
}

func newProfileSwitchCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "switch <name-or-id>",
		Short: "Switch the active profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := initDB(cfg)
			if err != nil {
				return err
			}
			defer db.Close()

			nameOrID := args[0]
			// Try by exact ID first, then by name (case-insensitive).
			var id string
			err = db.DB.QueryRow(`SELECT id FROM profiles WHERE id = ? OR LOWER(name) = LOWER(?) LIMIT 1`,
				nameOrID, nameOrID).Scan(&id)
			if err != nil {
				return fmt.Errorf("profile %q not found", nameOrID)
			}

			_, err = db.DB.Exec(`INSERT OR REPLACE INTO settings (key, value) VALUES ('active_profile_id', ?)`, id)
			if err != nil {
				return fmt.Errorf("switch profile: %w", err)
			}
			if cfg.JSONOutput {
				return printJSON(map[string]string{"id": id})
			}
			fmt.Printf("Switched to profile: %s\n", id)
			return nil
		},
	}
}

func newProfileCurrentCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "current",
		Short: "Show the active profile",
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := initDB(cfg)
			if err != nil {
				return err
			}
			defer db.Close()

			var id, name string
			err = db.DB.QueryRow(`SELECT p.id, p.name FROM profiles p
			                      INNER JOIN settings s ON s.value = p.id AND s.key = 'active_profile_id'`).Scan(&id, &name)
			if err != nil {
				if cfg.JSONOutput {
					return printJSON(map[string]string{"id": "default", "name": "default"})
				}
				fmt.Println("default")
				return nil
			}
			if cfg.JSONOutput {
				return printJSON(map[string]string{"id": id, "name": name})
			}
			fmt.Printf("%s (%s)\n", name, id)
			return nil
		},
	}
}
