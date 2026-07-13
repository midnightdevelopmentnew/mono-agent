package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"monoagent/internal/connections"
	"monoagent/internal/secrets"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
)

// newSecretCmd returns the `secret` command group: an encrypted vault for
// arbitrary API keys/passwords (kind "secret") and website logins (kind
// "login"). Plaintext is only ever returned by `secret reveal --reveal`;
// every other subcommand deals in names/references only.
func newSecretCmd(cfg *globalConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "Manage encrypted secrets and logins in the vault",
	}
	cmd.AddCommand(
		newSecretAddCmd(cfg),
		newSecretListCmd(cfg),
		newSecretGetCmd(cfg),
		newSecretRevealCmd(cfg),
		newSecretRmCmd(cfg),
		newSecretEncryptConnectionsCmd(cfg),
	)
	return cmd
}

func newSecretAddCmd(cfg *globalConfig) *cobra.Command {
	var kind, name, value, username, url, notes string
	cmd := &cobra.Command{
		Use:     "add",
		Short:   "Add a new secret or login to the vault",
		Example: `  monoagentcli secret add --kind secret --name openai-key
  monoagentcli secret add --kind login --name github --username alice --url https://github.com`,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()

			profileID := cfg.ProfileID
			if profileID == "" {
				profileID = "default"
			}

			if value == "" {
				fmt.Fprint(os.Stderr, "Value: ")
				reader := bufio.NewReader(os.Stdin)
				line, err := reader.ReadString('\n')
				if err != nil {
					return fmt.Errorf("reading value from stdin: %w", err)
				}
				value = strings.TrimRight(line, "\r\n")
			}

			id, err := secrets.Add(cmd.Context(), db.DB, profileID, kind, name, value, username, url, notes)
			if err != nil {
				return fmt.Errorf("adding secret: %w", err)
			}
			if cfg.JSONOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]string{"id": id, "name": name})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Added %s %q as %s.\n", kind, name, id)
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "secret", "Entry kind: secret or login")
	cmd.Flags().StringVar(&name, "name", "", "Unique name for this entry (required)")
	cmd.Flags().StringVar(&value, "value", "", "Secret/password value (omit to be prompted on stdin)")
	cmd.Flags().StringVar(&username, "username", "", "Username (login kind only)")
	cmd.Flags().StringVar(&url, "url", "", "URL (login kind only)")
	cmd.Flags().StringVar(&notes, "notes", "", "Optional notes")
	cmd.MarkFlagRequired("name")
	return cmd
}

func newSecretListCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List vault entries (metadata only — never secret values)",
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()

			profileID := cfg.ProfileID
			if profileID == "" {
				profileID = "default"
			}
			entries, err := secrets.List(cmd.Context(), db.DB, profileID)
			if err != nil {
				return fmt.Errorf("listing secrets: %w", err)
			}

			if cfg.JSONOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if entries == nil {
					entries = []secrets.Entry{}
				}
				return enc.Encode(entries)
			}
			if len(entries) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No secrets stored.")
				return nil
			}
			table := tablewriter.NewWriter(cmd.OutOrStdout())
			table.SetHeader([]string{"ID", "Kind", "Name", "Username", "Updated"})
			table.SetBorder(false)
			for _, e := range entries {
				table.Append([]string{e.ID, e.Kind, e.Name, e.Username, e.UpdatedAt})
			}
			table.Render()
			return nil
		},
	}
}

func newSecretGetCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:     "get <name>",
		Short:   "Resolve a vault reference for use in workflow configs (never returns plaintext)",
		Args:    cobra.ExactArgs(1),
		Example: `  monoagentcli secret get openai-key`,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()

			profileID := cfg.ProfileID
			if profileID == "" {
				profileID = "default"
			}
			entries, err := secrets.List(cmd.Context(), db.DB, profileID)
			if err != nil {
				return fmt.Errorf("looking up secret: %w", err)
			}
			for _, e := range entries {
				if e.Name == args[0] {
					ref := "@secret:" + e.Name
					if cfg.JSONOutput {
						enc := json.NewEncoder(cmd.OutOrStdout())
						enc.SetIndent("", "  ")
						return enc.Encode(map[string]string{"ref": ref, "id": e.ID})
					}
					fmt.Fprintln(cmd.OutOrStdout(), ref)
					return nil
				}
			}
			return fmt.Errorf("no secret named %q found", args[0])
		},
	}
}

func newSecretRevealCmd(cfg *globalConfig) *cobra.Command {
	var confirm bool
	cmd := &cobra.Command{
		Use:     "reveal <name>",
		Short:   "Print the plaintext value of a vault entry — requires --reveal to confirm",
		Args:    cobra.ExactArgs(1),
		Example: `  monoagentcli secret reveal openai-key --reveal`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !confirm {
				return fmt.Errorf("refusing to print a secret without --reveal (pass it explicitly to confirm)")
			}
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()

			profileID := cfg.ProfileID
			if profileID == "" {
				profileID = "default"
			}
			entries, err := secrets.List(cmd.Context(), db.DB, profileID)
			if err != nil {
				return fmt.Errorf("looking up secret: %w", err)
			}
			var id string
			for _, e := range entries {
				if e.Name == args[0] {
					id = e.ID
					break
				}
			}
			if id == "" {
				return fmt.Errorf("no secret named %q found", args[0])
			}
			value, err := secrets.DecryptEntry(cmd.Context(), db.DB, profileID, id)
			if err != nil {
				return fmt.Errorf("revealing secret: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), value)
			return nil
		},
	}
	cmd.Flags().BoolVar(&confirm, "reveal", false, "Confirm you want the plaintext value printed to stdout")
	return cmd
}

func newSecretRmCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:     "rm <name>",
		Short:   "Delete a vault entry",
		Args:    cobra.ExactArgs(1),
		Example: `  monoagentcli secret rm openai-key`,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()

			profileID := cfg.ProfileID
			if profileID == "" {
				profileID = "default"
			}
			entries, err := secrets.List(cmd.Context(), db.DB, profileID)
			if err != nil {
				return fmt.Errorf("looking up secret: %w", err)
			}
			var id string
			for _, e := range entries {
				if e.Name == args[0] {
					id = e.ID
					break
				}
			}
			if id == "" {
				return fmt.Errorf("no secret named %q found", args[0])
			}
			if err := secrets.Delete(cmd.Context(), db.DB, profileID, id); err != nil {
				return fmt.Errorf("deleting secret: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Deleted %q.\n", args[0])
			return nil
		},
	}
}

func newSecretEncryptConnectionsCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "encrypt-connections",
		Short: "One-time migration: encrypt any existing plaintext connection credentials in place",
		Long:  "Existing connections created before the secrets vault shipped store OAuth tokens/API keys as plaintext JSON. This re-saves every connection through the same Save path new connections already use, which now encrypts the data column automatically. Safe to run repeatedly — already-encrypted rows are re-encrypted (a no-op in effect) rather than skipped, since Save always re-applies the current encryption path.",
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()

			store := connections.NewStore(db.DB)
			if err := store.EnsureTable(cmd.Context()); err != nil {
				return fmt.Errorf("ensuring connections table: %w", err)
			}

			// Store.ListAll filters to an exact profile_id match, so there is
			// no single call that returns connections across every profile —
			// find the distinct profile IDs first and migrate each in turn.
			rows, err := db.DB.QueryContext(cmd.Context(), `SELECT DISTINCT COALESCE(profile_id,'default') FROM connections`)
			if err != nil {
				return fmt.Errorf("listing connection profiles: %w", err)
			}
			var profileIDs []string
			for rows.Next() {
				var profileID string
				if err := rows.Scan(&profileID); err != nil {
					rows.Close()
					return fmt.Errorf("scanning profile id: %w", err)
				}
				profileIDs = append(profileIDs, profileID)
			}
			if err := rows.Close(); err != nil {
				return fmt.Errorf("listing connection profiles: %w", err)
			}
			if err := rows.Err(); err != nil {
				return fmt.Errorf("listing connection profiles: %w", err)
			}

			var conns []connections.Connection
			for _, profileID := range profileIDs {
				profileConns, err := store.ListAll(cmd.Context(), profileID)
				if err != nil {
					return fmt.Errorf("listing connections for profile %q: %w", profileID, err)
				}
				conns = append(conns, profileConns...)
			}

			migrated := 0
			for i := range conns {
				if err := store.Save(cmd.Context(), &conns[i]); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to re-encrypt connection %s: %v\n", conns[i].ID, err)
					continue
				}
				migrated++
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Encrypted %d of %d connection(s).\n", migrated, len(conns))
			return nil
		},
	}
}
