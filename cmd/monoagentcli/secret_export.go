package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"monoagent/internal/secrets"

	"github.com/spf13/cobra"
)

func newSecretExportCmd(cfg *globalConfig) *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export the vault to an encrypted file, protected by a freshly generated passphrase",
		Example: `  monoagentcli secret export
  monoagentcli secret export --output ./my-vault.json.enc`,
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

			path := output
			if path == "" {
				path = fmt.Sprintf("vault-export-%s.json.enc", time.Now().UTC().Format("20060102-150405"))
			}

			passphrase, err := secrets.GenerateExportPassword()
			if err != nil {
				return fmt.Errorf("generating export passphrase: %w", err)
			}
			data, exported, skipped, err := secrets.Export(cmd.Context(), db.DB, profileID, passphrase)
			if err != nil {
				return fmt.Errorf("exporting vault: %w", err)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				return fmt.Errorf("writing export file: %w", err)
			}

			fmt.Fprintf(os.Stderr, "Vault exported to %s\n", path)
			fmt.Fprintf(os.Stderr, "Passphrase (save this now, it will not be shown again): %s\n", passphrase)
			fmt.Fprintf(os.Stderr, "Exported %d, skipped %d that could not be decrypted.\n", exported, skipped)

			if cfg.JSONOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]interface{}{"path": path, "passphrase": passphrase, "exported": exported, "skipped": skipped})
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&output, "output", "", "Output file path (default: vault-export-<timestamp>.json.enc in the current directory)")
	return cmd
}

func newSecretImportCmd(cfg *globalConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "import <file>",
		Short:   "Import entries from an encrypted vault export file",
		Args:    cobra.ExactArgs(1),
		Example: `  monoagentcli secret import ./my-vault.json.enc`,
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("reading export file: %w", err)
			}

			fmt.Fprint(os.Stderr, "Passphrase: ")
			reader := bufio.NewReader(os.Stdin)
			line, err := reader.ReadString('\n')
			if err != nil {
				return fmt.Errorf("reading passphrase from stdin: %w", err)
			}
			passphrase := strings.TrimRight(line, "\r\n")

			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()

			profileID := cfg.ProfileID
			if profileID == "" {
				profileID = "default"
			}

			imported, skipped, err := secrets.Import(cmd.Context(), db.DB, profileID, passphrase, data, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("importing vault: %w", err)
			}

			if cfg.JSONOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]int{"imported": imported, "skipped": skipped})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Imported %d, skipped %d duplicate(s).\n", imported, skipped)
			return nil
		},
	}
	return cmd
}
