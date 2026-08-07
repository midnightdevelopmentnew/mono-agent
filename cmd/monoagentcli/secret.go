package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"monoagent/internal/connections"
	"monoagent/internal/secrets"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
)

// newSecretCmd returns the `secret` command group: an encrypted vault for
// arbitrary API keys/passwords (kind "secret") and website logins (kind
// "login"), each holding one or more named fields. Plaintext is only ever
// returned by `secret reveal --reveal`; every other subcommand deals in
// names/references only.
func newSecretCmd(cfg *globalConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "Manage encrypted secrets and logins in the vault",
	}
	cmd.AddCommand(
		newSecretAddCmd(cfg),
		newSecretListCmd(cfg),
		newSecretGetCmd(cfg),
		newRevealCmd(cfg),
		newSecretUpdateCmd(cfg),
		newSecretRmCmd(cfg),
		newSecretEncryptConnectionsCmd(cfg),
		newSecretExportCmd(cfg),
		newSecretImportCmd(cfg),
	)
	return cmd
}

// lookupSecretID resolves a vault entry name to its internal id, scoped to
// profileID. Shared by reveal/update/rm, which all take a human-readable
// name on the command line but store/query by id underneath.
func lookupSecretID(ctx context.Context, db *sql.DB, profileID, name string) (string, error) {
	entries, err := secrets.List(ctx, db, profileID)
	if err != nil {
		return "", fmt.Errorf("looking up secret: %w", err)
	}
	for _, e := range entries {
		if e.Name == name {
			return e.ID, nil
		}
	}
	return "", fmt.Errorf("no secret named %q found", name)
}

// parseFieldFlags turns repeated "key=value" strings into a field map,
// splitting each on the first "=" only so values containing "=" (e.g. a
// connection string) round-trip correctly.
func parseFieldFlags(fieldFlags []string) (map[string]string, error) {
	fields := map[string]string{}
	for _, f := range fieldFlags {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			return nil, fmt.Errorf("invalid --field %q: expected key=value", f)
		}
		if k == "" {
			return nil, fmt.Errorf("invalid --field %q: key must not be empty", f)
		}
		if _, exists := fields[k]; exists {
			return nil, fmt.Errorf("duplicate field key %q", k)
		}
		fields[k] = v
	}
	return fields, nil
}

func newSecretAddCmd(cfg *globalConfig) *cobra.Command {
	var kind, name, value, username, url, notes string
	var fieldFlags []string
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add a new secret or login to the vault",
		Example: `  monoagentcli secret add --kind secret --name openai-key
  monoagentcli secret add --kind login --name github --username alice --url https://github.com
  monoagentcli secret add --kind secret --name aws --field access_key_id=... --field secret_access_key=...`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if value != "" && len(fieldFlags) > 0 {
				return fmt.Errorf("cannot use --value together with --field; --value is shorthand for --field secret=<value>")
			}

			fields, err := parseFieldFlags(fieldFlags)
			if err != nil {
				return err
			}
			if value != "" {
				fields["secret"] = value
			}
			if len(fields) == 0 {
				fmt.Fprint(os.Stderr, "Value: ")
				reader := bufio.NewReader(os.Stdin)
				line, err := reader.ReadString('\n')
				if err != nil {
					return fmt.Errorf("reading value from stdin: %w", err)
				}
				fields["secret"] = strings.TrimRight(line, "\r\n")
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

			id, err := secrets.Add(cmd.Context(), db.DB, profileID, kind, name, fields, username, url, notes)
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
	cmd.Flags().StringVar(&value, "value", "", "Shorthand for --field secret=<value> (omit both --value and --field to be prompted on stdin)")
	cmd.Flags().StringArrayVar(&fieldFlags, "field", nil, "Field as key=value (repeatable)")
	cmd.Flags().StringVar(&username, "username", "", "Username (login kind only)")
	cmd.Flags().StringVar(&url, "url", "", "URL (login kind only)")
	cmd.Flags().StringVar(&notes, "notes", "", "Optional notes")
	cmd.MarkFlagRequired("name")
	return cmd
}

func newSecretListCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List vault entries (metadata only — never field values)",
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
			table.SetHeader([]string{"ID", "Kind", "Name", "Username", "Fields", "Updated"})
			table.SetBorder(false)
			for _, e := range entries {
				table.Append([]string{e.ID, e.Kind, e.Name, e.Username, fmt.Sprintf("%d", e.FieldCount), e.UpdatedAt})
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
				if e.Name != args[0] {
					continue
				}
				ref := workflowRefPrefix + e.Name
				if cfg.JSONOutput {
					enc := json.NewEncoder(cmd.OutOrStdout())
					enc.SetIndent("", "  ")
					return enc.Encode(map[string]string{"ref": ref, "id": e.ID})
				}
				fmt.Fprintln(cmd.OutOrStdout(), ref)
				return nil
			}
			return fmt.Errorf("no secret named %q found", args[0])
		},
	}
}

// newRevealCmd's name stays `newRevealCmd` rather than following the
// `newSecret*Cmd` pattern the sibling constructors use — a deliberate,
// minor exception to keep this one identifier distinct from the others.
func newRevealCmd(cfg *globalConfig) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "reveal <name>",
		Short: "Print field value(s) - use --reveal to confirm",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				return fmt.Errorf("pass --reveal explicitly to print field value(s)")
			}
			return revealAndPrint(cfg, cmd, args[0])
		},
	}
	cmd.Flags().BoolVar(&yes, "reveal", false, "Confirm the print")
	return cmd
}

func revealAndPrint(cfg *globalConfig, cmd *cobra.Command, name string) error {
	db, err := initDB(cfg)
	if err != nil {
		return fmt.Errorf("initializing database: %w", err)
	}
	defer db.DB.Close()

	profileID := cfg.ProfileID
	if profileID == "" {
		profileID = "default"
	}
	id, err := lookupSecretID(cmd.Context(), db.DB, profileID, name)
	if err != nil {
		return err
	}
	fields, notes, err := secrets.DecryptFields(cmd.Context(), db.DB, profileID, id)
	if err != nil {
		return fmt.Errorf("decrypting entry: %w", err)
	}
	return printRevealedFields(cmd, cfg, fields, notes)
}

// printRevealedFields: text mode prints a bare value for a single field
// (matching the pre-key-value behavior for the common case) or `key: value`
// lines for several; JSON mode always emits both fields and notes as one
// object.
func printRevealedFields(cmd *cobra.Command, cfg *globalConfig, fields map[string]string, notes string) error {
	if cfg.JSONOutput {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]interface{}{"fields": fields, "notes": notes})
	}
	if len(fields) == 1 {
		for _, v := range fields {
			fmt.Fprintln(cmd.OutOrStdout(), v)
		}
		return nil
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", k, fields[k])
	}
	return nil
}

func newSecretUpdateCmd(cfg *globalConfig) *cobra.Command {
	var newName, username, url, notes string
	var fieldFlags []string
	cmd := &cobra.Command{
		Use:     "update <name>",
		Short:   "Update an existing vault entry's metadata and/or fields",
		Args:    cobra.ExactArgs(1),
		Example: `  monoagentcli secret update github --username bob`,
		RunE:    runSecretUpdate(cfg, &newName, &username, &url, &notes, &fieldFlags),
	}
	cmd.Flags().StringVar(&newName, "name", "", "Rename this entry")
	cmd.Flags().StringVar(&username, "username", "", "New username (login kind only)")
	cmd.Flags().StringVar(&url, "url", "", "New URL (login kind only)")
	cmd.Flags().StringVar(&notes, "notes", "", "New notes")
	cmd.Flags().StringArrayVar(&fieldFlags, "field", nil, "Replace the entire field set with these key=value pairs (repeatable)")
	return cmd
}

func runSecretUpdate(cfg *globalConfig, newName, username, url, notes *string, fieldFlags *[]string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		db, err := initDB(cfg)
		if err != nil {
			return fmt.Errorf("initializing database: %w", err)
		}
		defer db.DB.Close()

		profileID := cfg.ProfileID
		if profileID == "" {
			profileID = "default"
		}
		id, err := lookupSecretID(cmd.Context(), db.DB, profileID, args[0])
		if err != nil {
			return err
		}

		var namePtr, usernamePtr, urlPtr, notesPtr *string
		if cmd.Flags().Changed("name") {
			namePtr = newName
		}
		if cmd.Flags().Changed("username") {
			usernamePtr = username
		}
		if cmd.Flags().Changed("url") {
			urlPtr = url
		}
		if cmd.Flags().Changed("notes") {
			notesPtr = notes
		}

		var fields map[string]string
		if len(*fieldFlags) > 0 {
			fields, err = parseFieldFlags(*fieldFlags)
			if err != nil {
				return err
			}
		}

		if err := secrets.Update(cmd.Context(), db.DB, profileID, id, namePtr, usernamePtr, urlPtr, notesPtr, fields); err != nil {
			return fmt.Errorf("updating entry: %w", err)
		}

		finalName := args[0]
		if namePtr != nil {
			finalName = *namePtr
		}
		if cfg.JSONOutput {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(map[string]string{"name": finalName})
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Updated %q.\n", finalName)
		return nil
	}
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
			id, err := lookupSecretID(cmd.Context(), db.DB, profileID, args[0])
			if err != nil {
				return err
			}
			if err := secrets.DeleteCascade(cmd.Context(), db.DB, profileID, id); err != nil {
				return fmt.Errorf("deleting entry: %w", err)
			}
			if cfg.JSONOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]string{"name": args[0]})
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
		Long:  "Existing connections created before the secrets vault shipped store OAuth tokens/API keys as plaintext JSON. This re-saves every such connection through the same Save path new connections already use, which encrypts the data column automatically. Safe to run repeatedly — it's a no-op once every row is encrypted. This same check-and-migrate step also runs automatically on every CLI and GUI startup, so running this command by hand is normally unnecessary.",
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()

			migrated, total, err := connections.MigrateConnectionsToVault(cmd.Context(), db.DB)
			if err != nil {
				return fmt.Errorf("encrypting connections: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Encrypted %d of %d connection(s).\n", migrated, total)
			return nil
		},
	}
}

// workflowRefPrefix mirrors internal/secrets/resolve.go's own
// secretRefPrefix constant byte-for-byte (that file's Resolve is what
// actually interprets this prefix) — duplicated here as a plain string
// rather than exported from the secrets package, since nothing else in the
// CLI needs it.
const workflowRefPrefix = "@secret:"
