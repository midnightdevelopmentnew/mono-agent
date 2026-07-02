package main

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

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

			rows, err := db.DB.Query(`SELECT id, name, created_at FROM profiles ORDER BY created_at ASC`)
			if err != nil {
				return fmt.Errorf("list profiles: %w", err)
			}
			defer rows.Close()

			var activeID string
			_ = db.DB.QueryRow(`SELECT value FROM settings WHERE key = 'active_profile_id'`).Scan(&activeID)

			fmt.Printf("%-36s  %-20s  %s\n", "ID", "NAME", "CREATED")
			for rows.Next() {
				var id, name, createdAt string
				if rows.Scan(&id, &name, &createdAt) == nil {
					marker := ""
					if id == activeID {
						marker = " *"
					}
					fmt.Printf("%-36s  %-20s  %s%s\n", id, name, createdAt, marker)
				}
			}
			return rows.Err()
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
				fmt.Println("default")
				return nil
			}
			fmt.Printf("%s (%s)\n", name, id)
			return nil
		},
	}
}
