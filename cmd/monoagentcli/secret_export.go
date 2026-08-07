package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"monoagent/internal/ai"
	"monoagent/internal/connections"
	"monoagent/internal/secrets"

	"github.com/google/uuid"
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

			imported, skipped, err := secrets.Import(cmd.Context(), db.DB, profileID, passphrase, data,
				rematerializeConnection, rematerializeSession, rematerializeProvider)
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

// rematerializeConnection reconnects an imported vault entry to a
// connections row on this machine, matched by platform+label against what's
// already present.
func rematerializeConnection(ctx context.Context, db *sql.DB, profileID, vaultID, name string, meta map[string]string) error {
	store := connections.NewStore(db)
	existing, err := store.ListByPlatform(ctx, meta["platform"], profileID)
	if err != nil {
		return fmt.Errorf("checking for an existing connection: %w", err)
	}
	conn := &connections.Connection{
		Platform:  meta["platform"],
		Method:    connections.AuthMethod(meta["method"]),
		Label:     meta["label"],
		AccountID: meta["account_id"],
		ProfileID: profileID,
		VaultRef:  vaultID,
		Data:      map[string]interface{}{},
	}
	for _, e := range existing {
		if e.Label == meta["label"] {
			conn.ID = e.ID
			existingConn, getErr := store.Get(ctx, e.ID)
			if getErr != nil {
				return fmt.Errorf("loading the existing connection to preserve its data: %w", getErr)
			}
			if existingConn != nil {
				conn.Data = existingConn.Data
			}
			break
		}
	}
	return store.Save(ctx, conn)
}

// rematerializeSession reconnects an imported vault entry (which already
// holds the actual cookie jar as its "cookies" field) to a crawler_sessions
// row, matched by platform+username, mirroring the UPDATE-then-INSERT
// upsert upsertSessionRow (login.go) uses.
func rematerializeSession(ctx context.Context, db *sql.DB, profileID, vaultID, name string, meta map[string]string) error {
	expiry := time.Now().Add(30 * 24 * time.Hour)
	res, err := db.ExecContext(ctx,
		`UPDATE crawler_sessions SET vault_ref = ?, expiry = ? WHERE username = ? AND platform = ? AND profile_id = ?`,
		vaultID, expiry, meta["username"], meta["platform"], profileID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_, err = db.ExecContext(ctx,
			`INSERT INTO crawler_sessions (username, platform, cookies_json, vault_ref, expiry, profile_id) VALUES (?, ?, '', ?, ?, ?)`,
			meta["username"], meta["platform"], vaultID, expiry, profileID)
	}
	return err
}

// rematerializeProvider reconnects an imported vault entry to an AI
// provider row, matched by provider name. It deliberately leaves the
// returned AIProvider's credential field at its zero value — the vault
// entry (vaultID, already imported by Import before this callback runs) is
// the credential; SaveProvider's vault-write branch only fires when that
// field is non-empty, so this just persists p.VaultRef as given.
func rematerializeProvider(ctx context.Context, db *sql.DB, profileID, vaultID, name string, meta map[string]string) error {
	store, err := ai.NewAIStore(db)
	if err != nil {
		return fmt.Errorf("opening AI store: %w", err)
	}
	existing, err := store.ListProviders(profileID)
	if err != nil {
		return fmt.Errorf("checking for an existing provider: %w", err)
	}
	p := ai.AIProvider{
		Name:         name,
		ProviderID:   meta["provider_id"],
		Tier:         meta["tier"],
		BaseURL:      meta["base_url"],
		DefaultModel: meta["default_model"],
		ExtraHeaders: meta["extra_headers"],
		ProfileID:    profileID,
		VaultRef:     vaultID,
	}
	for _, e := range existing {
		if e.Name == name {
			p.ID = e.ID
			p.Status = e.Status
			p.LastTested = e.LastTested
			break
		}
	}
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	return store.SaveProvider(p)
}
