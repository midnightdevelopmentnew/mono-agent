package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"monoagent/internal/storage"
	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
)

func newPeopleMessagesCmd(cfg *globalConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "messages",
		Aliases: []string{"history"},
		Short:   "Manage a person's message/interaction history",
		Long:    "Store and list messages or interactions for a person, ingested from any source (Outlook, social platforms, manual notes, ...).",
	}

	cmd.AddCommand(
		newPeopleMessagesAddCmd(cfg),
		newPeopleMessagesListCmd(cfg),
		newPeopleMessagesImportCmd(cfg),
	)

	return cmd
}

func newPeopleMessagesAddCmd(cfg *globalConfig) *cobra.Command {
	var (
		source     string
		externalID string
		direction  string
		sender     string
		subject    string
		body       string
		sentAt     string
	)

	cmd := &cobra.Command{
		Use:   "add <person-id>",
		Short: "Record a single message/interaction for a person",
		Args:  cobra.ExactArgs(1),
		Example: `  monoagentcli people messages add abc123 --source outlook --subject "Re: intro" --body "Thanks for reaching out"
  monoagentcli people messages add abc123 --source instagram --direction outbound --body "hey!"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if source == "" {
				return fmt.Errorf("--source is required")
			}
			if direction == "" {
				direction = "inbound"
			}

			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			msg := &storage.PersonMessage{
				PersonID:   args[0],
				Source:     source,
				ExternalID: externalID,
				Direction:  direction,
				Sender:     sender,
				Subject:    subject,
				Body:       body,
			}
			if sentAt != "" {
				t, err := time.Parse(time.RFC3339, sentAt)
				if err != nil {
					return fmt.Errorf("parsing --sent-at (expected RFC3339): %w", err)
				}
				msg.SentAt = t
			}

			if err := db.UpsertPersonMessage(msg, cfg.ProfileID); err != nil {
				return fmt.Errorf("saving message: %w", err)
			}

			fmt.Fprintf(os.Stdout, "Saved message %s for person %s.\n", msg.ID, msg.PersonID)
			return nil
		},
	}

	cmd.Flags().StringVar(&source, "source", "", "Source of the message, e.g. outlook, gmail, instagram, linkedin, x, telegram, manual (required)")
	cmd.Flags().StringVar(&externalID, "external-id", "", "Source-native message/thread id, for idempotent re-import")
	cmd.Flags().StringVar(&direction, "direction", "inbound", "Message direction: inbound or outbound")
	cmd.Flags().StringVar(&sender, "sender", "", "Sender name or address")
	cmd.Flags().StringVar(&subject, "subject", "", "Message subject")
	cmd.Flags().StringVar(&body, "body", "", "Message body")
	cmd.Flags().StringVar(&sentAt, "sent-at", "", "When the message was sent, RFC3339 (defaults to now)")
	_ = cmd.MarkFlagRequired("source")

	return cmd
}

func newPeopleMessagesListCmd(cfg *globalConfig) *cobra.Command {
	var (
		source string
		limit  int
	)

	cmd := &cobra.Command{
		Use:   "list <person-id>",
		Short: "List a person's message/interaction history",
		Args:  cobra.ExactArgs(1),
		Example: `  monoagentcli people messages list abc123
  monoagentcli people messages list abc123 --source outlook --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			messages, err := db.ListPersonMessages(args[0], source, cfg.ProfileID, limit, 0)
			if err != nil {
				return fmt.Errorf("listing messages: %w", err)
			}

			if cfg.JSONOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(messages)
			}

			if len(messages) == 0 {
				fmt.Println("No messages found.")
				return nil
			}

			table := tablewriter.NewWriter(os.Stdout)
			table.SetHeader([]string{"ID", "Source", "Direction", "Sender", "Subject", "Sent At"})
			table.SetBorder(false)
			table.SetAutoWrapText(false)

			for _, m := range messages {
				shortID := m.ID
				if len(shortID) > 8 {
					shortID = shortID[:8]
				}
				sentAt := ""
				if !m.SentAt.IsZero() {
					sentAt = m.SentAt.Format("2006-01-02 15:04:05")
				}
				table.Append([]string{
					shortID, m.Source, m.Direction,
					truncateStr(m.Sender, 20), truncateStr(m.Subject, 30), sentAt,
				})
			}
			table.Render()
			fmt.Fprintf(os.Stderr, "\nTotal: %d message(s)\n", len(messages))
			return nil
		},
	}

	cmd.Flags().StringVar(&source, "source", "", "Filter by source")
	cmd.Flags().IntVarP(&limit, "limit", "n", 100, "Maximum number of results")

	return cmd
}

func newPeopleMessagesImportCmd(cfg *globalConfig) *cobra.Command {
	var (
		filePath string
		source   string
	)

	cmd := &cobra.Command{
		Use:   "import <person-id>",
		Short: "Bulk-import a person's message history from a JSON array file",
		Args:  cobra.ExactArgs(1),
		Example: `  monoagentcli people messages import abc123 --file outlook_thread.json --source outlook`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if filePath == "" {
				return fmt.Errorf("--file is required")
			}
			if source == "" {
				return fmt.Errorf("--source is required")
			}

			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			data, err := os.ReadFile(filePath)
			if err != nil {
				return fmt.Errorf("reading file %s: %w", filePath, err)
			}

			var raw []map[string]interface{}
			if err := json.Unmarshal(data, &raw); err != nil {
				return fmt.Errorf("parsing JSON array: %w", err)
			}

			var imported int
			for _, r := range raw {
				msg := &storage.PersonMessage{
					PersonID:   args[0],
					Source:     source,
					ExternalID: getStr(r, "external_id"),
					Direction:  getStr(r, "direction"),
					Sender:     getStr(r, "sender"),
					Subject:    getStr(r, "subject"),
					Body:       getStr(r, "body"),
				}
				if v := getStr(r, "sent_at"); v != "" {
					if t, err := time.Parse(time.RFC3339, v); err == nil {
						msg.SentAt = t
					}
				}
				if err := db.UpsertPersonMessage(msg, cfg.ProfileID); err != nil {
					return fmt.Errorf("importing message: %w", err)
				}
				imported++
			}

			fmt.Fprintf(os.Stdout, "Imported %d message(s) for person %s from %s.\n", imported, args[0], source)
			return nil
		},
	}

	cmd.Flags().StringVar(&filePath, "file", "", "Path to JSON file containing a message array (required)")
	cmd.Flags().StringVar(&source, "source", "", "Source of the messages, e.g. outlook, gmail, instagram (required)")
	_ = cmd.MarkFlagRequired("file")
	_ = cmd.MarkFlagRequired("source")

	return cmd
}

func getStr(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}
