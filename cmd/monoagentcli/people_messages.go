package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"monoagent/internal/connections"
	"monoagent/internal/storage"
	"monoagent/internal/workflow"
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
		newPeopleMessagesAllCmd(cfg),
		newPeopleMessagesComposeCmd(cfg),
		newPeopleMessagesReplyCmd(cfg),
		newPeopleMessagesDraftsCmd(cfg),
		newPeopleMessagesSendDraftCmd(cfg),
		newPeopleMessagesRejectDraftCmd(cfg),
	)

	return cmd
}

func newPeopleMessagesAllCmd(cfg *globalConfig) *cobra.Command {
	var (
		source string
		limit  int
	)

	cmd := &cobra.Command{
		Use:   "all",
		Short: "List synced messages/interactions across every person (a unified communications feed)",
		Example: `  monoagentcli people messages all
  monoagentcli people messages all --source outlook --limit 50 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			messages, err := db.ListAllPersonMessages(cfg.ProfileID, source, limit, 0)
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
			table.SetHeader([]string{"ID", "From", "Source", "Direction", "Subject", "Sent At"})
			table.SetBorder(false)
			table.SetAutoWrapText(false)

			for _, m := range messages {
				shortID := m.ID
				if len(shortID) > 8 {
					shortID = shortID[:8]
				}
				from := m.PersonFullName
				if from == "" {
					from = m.PersonPlatformUsername
				}
				sentAt := ""
				if !m.SentAt.IsZero() {
					sentAt = m.SentAt.Format("2006-01-02 15:04:05")
				}
				table.Append([]string{
					shortID, truncateStr(from, 24), m.Source, m.Direction,
					truncateStr(m.Subject, 40), sentAt,
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

// newPeopleMessagesComposeCmd sends (or drafts) an email to a person via
// service.outlook_mail and records the result on their message history —
// the CLI equivalent of the GUI's Compose button on a person's Profile page.
func newPeopleMessagesComposeCmd(cfg *globalConfig) *cobra.Command {
	var (
		connectionID string
		subject      string
		body         string
		bodyType     string
		asDraft      bool
		toOverride   string
		cc           string
		bcc          string
		replyTo      string
		replyAll     bool
	)

	cmd := &cobra.Command{
		Use:   "compose <person-id>",
		Short: "Send or save-as-draft an email to a person, recorded on their message history",
		Args:  cobra.ExactArgs(1),
		Example: `  monoagentcli people messages compose abc123 --connection outlook --subject "Hi" --body "Thanks for reaching out"
  monoagentcli people messages compose abc123 --connection outlook --subject "Hi" --body "..." --draft
  monoagentcli people messages compose abc123 --connection outlook --subject "Hi" --body "..." --to "a@x.com,b@x.com"
  monoagentcli people messages compose abc123 --connection outlook --subject "Hi" --body "..." --cc "c@x.com,d@x.com"
  monoagentcli people messages compose abc123 --connection outlook --body "Sounds good" --reply-to <message-id> --reply-all`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if body == "" {
				return fmt.Errorf("--body is required")
			}

			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			// --reply-to threads this compose onto an existing conversation
			// via Graph's createReply/createReplyAll instead of starting a
			// fresh conversationId.
			if replyTo != "" {
				orig, err := db.GetPersonMessage(replyTo)
				if err != nil {
					return err
				}
				if orig == nil {
					return fmt.Errorf("message %s not found", replyTo)
				}
				if err := assertMessageInProfile(db.DB, replyTo, cfg.ProfileID); err != nil {
					return err
				}
				if orig.ExternalID == "" {
					return fmt.Errorf("message %s has no associated Outlook message id, cannot reply", replyTo)
				}

				operation, status := "reply", "sent"
				if asDraft {
					operation, status = "create_reply", "draft"
				}

				config := map[string]interface{}{
					"operation":  operation,
					"message_id": orig.ExternalID,
					"reply_all":  replyAll,
					"body":       body,
				}
				if toOverride != "" {
					config["to"] = toOverride
				}
				if cc != "" {
					config["cc"] = cc
				}
				if bcc != "" {
					config["bcc"] = bcc
				}
				outputs, err := runOutlookNode(cmd, cfg, db.DB, connectionID, config)
				if err != nil {
					return err
				}

				var externalID string
				if len(outputs) > 0 && len(outputs[0].Items) > 0 {
					externalID = getStr(outputs[0].Items[0].JSON, "id")
				}
				metaBytes, _ := json.Marshal(map[string]string{"connection_id": connectionID})

				replySubject := orig.Subject
				if replySubject != "" && replySubject[:min(4, len(replySubject))] != "Re: " {
					replySubject = "Re: " + replySubject
				}

				msg := &storage.PersonMessage{
					PersonID:   args[0],
					Source:     "outlook",
					ExternalID: externalID,
					Direction:  "outbound",
					Sender:     orig.Sender,
					Subject:    replySubject,
					Body:       body,
					Metadata:   string(metaBytes),
					Status:     status,
					SentAt:     time.Now().UTC(),
				}
				if err := db.UpsertPersonMessage(msg, cfg.ProfileID); err != nil {
					return fmt.Errorf("saving message: %w", err)
				}

				if cfg.JSONOutput {
					return json.NewEncoder(os.Stdout).Encode(msg)
				}
				verb := "Sent"
				if asDraft {
					verb = "Drafted"
				}
				what := "reply"
				if replyAll {
					what = "reply-all"
				}
				fmt.Fprintf(os.Stdout, "%s %s to message %s (new message id: %s)\n", verb, what, replyTo, msg.ID)
				return nil
			}

			var toAddr string
			if err := db.DB.QueryRow(
				`SELECT platform_username FROM people WHERE id = ? AND profile_id = ?`,
				args[0], cfg.ProfileID,
			).Scan(&toAddr); err != nil {
				return fmt.Errorf("person %s not found in profile %s: %w", args[0], cfg.ProfileID, err)
			}
			if toOverride != "" {
				toAddr = toOverride
			}

			operation, status := "send_message", "sent"
			if asDraft {
				operation, status = "create_draft", "draft"
			}
			if bodyType == "" {
				bodyType = "html"
			}

			config := map[string]interface{}{
				"operation": operation,
				"to":        toAddr,
				"subject":   subject,
				"body":      body,
				"body_type": bodyType,
			}
			if cc != "" {
				config["cc"] = cc
			}
			if bcc != "" {
				config["bcc"] = bcc
			}
			outputs, err := runOutlookNode(cmd, cfg, db.DB, connectionID, config)
			if err != nil {
				return err
			}

			var externalID string
			if len(outputs) > 0 && len(outputs[0].Items) > 0 {
				externalID = getStr(outputs[0].Items[0].JSON, "id")
			}
			metaBytes, _ := json.Marshal(map[string]string{"connection_id": connectionID})

			msg := &storage.PersonMessage{
				PersonID:   args[0],
				Source:     "outlook",
				ExternalID: externalID,
				Direction:  "outbound",
				Sender:     toAddr,
				Subject:    subject,
				Body:       body,
				Metadata:   string(metaBytes),
				Status:     status,
				SentAt:     time.Now().UTC(),
			}
			if err := db.UpsertPersonMessage(msg, cfg.ProfileID); err != nil {
				return fmt.Errorf("saving message: %w", err)
			}

			if cfg.JSONOutput {
				return json.NewEncoder(os.Stdout).Encode(msg)
			}
			verb := "Sent"
			if asDraft {
				verb = "Drafted"
			}
			fmt.Fprintf(os.Stdout, "%s message to %s (message id: %s)\n", verb, toAddr, msg.ID)
			return nil
		},
	}

	cmd.Flags().StringVar(&connectionID, "connection", "", "Outlook connection ID or platform name, e.g. \"outlook\" (required)")
	cmd.Flags().StringVar(&subject, "subject", "", "Subject")
	cmd.Flags().StringVar(&body, "body", "", "Body (required)")
	cmd.Flags().StringVar(&bodyType, "body-type", "html", "Body type: text or html")
	cmd.Flags().BoolVar(&asDraft, "draft", false, "Save as a draft instead of sending immediately")
	cmd.Flags().StringVar(&toOverride, "to", "", "Recipient address(es), comma-separated for multiple (default: the person's stored address; with --reply-to, added to the reply)")
	cmd.Flags().StringVar(&cc, "cc", "", "CC address(es), comma-separated for multiple")
	cmd.Flags().StringVar(&bcc, "bcc", "", "BCC address(es), comma-separated for multiple")
	cmd.Flags().StringVar(&replyTo, "reply-to", "", "Message id (from message history) to reply to, threading onto its existing conversation instead of starting a new one")
	cmd.Flags().BoolVar(&replyAll, "reply-all", false, "With --reply-to, reply to everyone on the original message instead of just the sender")
	_ = cmd.MarkFlagRequired("connection")
	_ = cmd.MarkFlagRequired("body")

	return cmd
}

// newPeopleMessagesReplyCmd replies to a previously recorded message
// (inbound or outbound) on the same Outlook thread, via service.outlook_mail's
// createReply/createReplyAll/reply/replyAll operations — the CLI equivalent
// of hitting Reply/Reply All on an email instead of composing a new one.
func newPeopleMessagesReplyCmd(cfg *globalConfig) *cobra.Command {
	var (
		connectionID string
		body         string
		asDraft      bool
		replyAll     bool
	)

	cmd := &cobra.Command{
		Use:   "reply <message-id>",
		Short: "Reply (or reply-all) on the same thread as a recorded message, recorded on message history",
		Args:  cobra.ExactArgs(1),
		Example: `  monoagentcli people messages reply abc123 --connection outlook --body "Sounds good, thanks!"
  monoagentcli people messages reply abc123 --connection outlook --body "Looping everyone in" --reply-all
  monoagentcli people messages reply abc123 --connection outlook --body "..." --draft`,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			msg, err := db.GetPersonMessage(args[0])
			if err != nil {
				return err
			}
			if msg == nil {
				return fmt.Errorf("message %s not found", args[0])
			}
			if err := assertMessageInProfile(db.DB, args[0], cfg.ProfileID); err != nil {
				return err
			}
			if msg.ExternalID == "" {
				return fmt.Errorf("message %s has no associated Outlook message id, cannot reply", args[0])
			}

			resolvedConnectionID := connectionID
			if resolvedConnectionID == "" {
				resolvedConnectionID, err = draftMessageConnectionID(msg)
				if err != nil {
					return fmt.Errorf("--connection is required (could not recover it from message history): %w", err)
				}
			}

			operation, status := "reply", "sent"
			if asDraft {
				operation, status = "create_reply", "draft"
			}

			config := map[string]interface{}{
				"operation":  operation,
				"message_id": msg.ExternalID,
				"reply_all":  replyAll,
			}
			if body != "" {
				config["body"] = body
			}
			outputs, err := runOutlookNode(cmd, cfg, db.DB, resolvedConnectionID, config)
			if err != nil {
				return err
			}

			var externalID string
			if len(outputs) > 0 && len(outputs[0].Items) > 0 {
				externalID = getStr(outputs[0].Items[0].JSON, "id")
			}
			metaBytes, _ := json.Marshal(map[string]string{"connection_id": resolvedConnectionID})

			subject := msg.Subject
			if subject != "" && subject[:min(4, len(subject))] != "Re: " {
				subject = "Re: " + subject
			}

			reply := &storage.PersonMessage{
				PersonID:   msg.PersonID,
				Source:     "outlook",
				ExternalID: externalID,
				Direction:  "outbound",
				Sender:     msg.Sender,
				Subject:    subject,
				Body:       body,
				Metadata:   string(metaBytes),
				Status:     status,
				SentAt:     time.Now().UTC(),
			}
			if err := db.UpsertPersonMessage(reply, cfg.ProfileID); err != nil {
				return fmt.Errorf("saving message: %w", err)
			}

			if cfg.JSONOutput {
				return json.NewEncoder(os.Stdout).Encode(reply)
			}
			verb := "Sent"
			if asDraft {
				verb = "Drafted"
			}
			what := "reply"
			if replyAll {
				what = "reply-all"
			}
			fmt.Fprintf(os.Stdout, "%s %s to message %s (new message id: %s)\n", verb, what, args[0], reply.ID)
			return nil
		},
	}

	cmd.Flags().StringVar(&connectionID, "connection", "", "Outlook connection ID or platform name, e.g. \"outlook\" (defaults to the connection that synced/sent the original message, if recorded)")
	cmd.Flags().StringVar(&body, "body", "", "Reply body/comment. Left empty, Outlook sends just the quoted original with no added text")
	cmd.Flags().BoolVar(&asDraft, "draft", false, "Save as a draft instead of sending immediately")
	cmd.Flags().BoolVar(&replyAll, "reply-all", false, "Reply to everyone on the original message instead of just the sender")

	return cmd
}

// newPeopleMessagesDraftsCmd lists draft messages awaiting confirmation —
// the same set shown in the GUI's Human in Loop page.
func newPeopleMessagesDraftsCmd(cfg *globalConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "drafts",
		Short: "List draft messages awaiting confirmation (also shown in the GUI's Human in Loop page)",
		Example: `  monoagentcli people messages drafts
  monoagentcli people messages drafts --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			drafts, err := db.ListPersonMessagesByStatus(cfg.ProfileID, "draft")
			if err != nil {
				return fmt.Errorf("listing drafts: %w", err)
			}

			if cfg.JSONOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(drafts)
			}

			if len(drafts) == 0 {
				fmt.Println("No pending drafts.")
				return nil
			}

			table := tablewriter.NewWriter(os.Stdout)
			table.SetHeader([]string{"ID", "To", "Subject", "Created"})
			table.SetBorder(false)
			table.SetAutoWrapText(false)
			for _, d := range drafts {
				to := d.PersonFullName
				if to == "" {
					to = d.PersonPlatformUsername
				}
				shortID := d.ID
				if len(shortID) > 8 {
					shortID = shortID[:8]
				}
				table.Append([]string{shortID, truncateStr(to, 24), truncateStr(d.Subject, 40), d.CreatedAt.Format("2006-01-02 15:04:05")})
			}
			table.Render()
			fmt.Fprintf(os.Stderr, "\nUse 'monoagentcli people messages send-draft <full-id>' or 'reject-draft <full-id>' (get the full ID with --json).\n")
			return nil
		},
	}
	return cmd
}

// newPeopleMessagesSendDraftCmd sends a previously-created draft as-is.
func newPeopleMessagesSendDraftCmd(cfg *globalConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "send-draft <message-id>",
		Short: "Send a previously-created draft message",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			msg, err := db.GetPersonMessage(args[0])
			if err != nil {
				return err
			}
			if msg == nil {
				return fmt.Errorf("message %s not found", args[0])
			}
			if err := assertMessageInProfile(db.DB, args[0], cfg.ProfileID); err != nil {
				return err
			}
			if msg.Status != "draft" {
				return fmt.Errorf("message %s is not a draft (status: %s)", args[0], msg.Status)
			}
			connectionID, err := draftMessageConnectionID(msg)
			if err != nil {
				return err
			}

			config := map[string]interface{}{"operation": "send_draft", "message_id": msg.ExternalID}
			outputs, err := runOutlookNode(cmd, cfg, db.DB, connectionID, config)
			if err != nil {
				return err
			}

			if err := db.UpdatePersonMessageStatus(args[0], "sent"); err != nil {
				return fmt.Errorf("updating status: %w", err)
			}
			// Graph reassigns a new message id when a draft is sent (moved
			// into Sent Items), so the stored external_id must be updated to
			// stay valid for a later reply/get_message/delete_message.
			if len(outputs) > 0 && len(outputs[0].Items) > 0 {
				if newID := getStr(outputs[0].Items[0].JSON, "message_id"); newID != "" && newID != msg.ExternalID {
					if err := db.UpdatePersonMessageExternalID(args[0], newID); err != nil {
						return fmt.Errorf("updating external id: %w", err)
					}
				}
			}
			fmt.Fprintf(os.Stdout, "Sent draft %s.\n", args[0])
			return nil
		},
	}
	return cmd
}

// newPeopleMessagesRejectDraftCmd discards a draft: best-effort removes it
// from the mailbox's Drafts folder too, then deletes the local history row.
func newPeopleMessagesRejectDraftCmd(cfg *globalConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reject-draft <message-id>",
		Short: "Discard a draft message",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			msg, err := db.GetPersonMessage(args[0])
			if err != nil {
				return err
			}
			if msg == nil {
				return fmt.Errorf("message %s not found", args[0])
			}
			if err := assertMessageInProfile(db.DB, args[0], cfg.ProfileID); err != nil {
				return err
			}
			if connectionID, cErr := draftMessageConnectionID(msg); cErr == nil && msg.ExternalID != "" {
				config := map[string]interface{}{"operation": "delete_message", "message_id": msg.ExternalID}
				_, _ = runOutlookNode(cmd, cfg, db.DB, connectionID, config) // best-effort
			}

			if err := db.DeletePersonMessage(args[0]); err != nil {
				return fmt.Errorf("deleting message: %w", err)
			}
			fmt.Fprintf(os.Stdout, "Discarded draft %s.\n", args[0])
			return nil
		},
	}
	return cmd
}

// assertMessageInProfile fails if the message does not belong to the active
// profile. GetPersonMessage/UpdatePersonMessageStatus/DeletePersonMessage look
// up by bare ID with no profile predicate, so without this check a draft owned
// by one profile could be sent or discarded from another.
func assertMessageInProfile(db *sql.DB, msgID, profileID string) error {
	if profileID == "" {
		profileID = "default"
	}
	var owner string
	err := db.QueryRow("SELECT profile_id FROM person_messages WHERE id = ?", msgID).Scan(&owner)
	if err == sql.ErrNoRows {
		return fmt.Errorf("message %s not found", msgID)
	}
	if err != nil {
		return err
	}
	if owner != profileID {
		return fmt.Errorf("message %s not found", msgID)
	}
	return nil
}

// draftMessageConnectionID extracts the connection_id stashed in a draft
// message's Metadata by newPeopleMessagesComposeCmd.
func draftMessageConnectionID(msg *storage.PersonMessage) (string, error) {
	var meta struct {
		ConnectionID string `json:"connection_id"`
	}
	if msg.Metadata == "" {
		return "", fmt.Errorf("message %s has no associated connection", msg.ID)
	}
	if err := json.Unmarshal([]byte(msg.Metadata), &meta); err != nil || meta.ConnectionID == "" {
		return "", fmt.Errorf("message %s has no associated connection", msg.ID)
	}
	return meta.ConnectionID, nil
}

// runOutlookNode resolves credentials for the given connection ID/platform
// name (scoped to cfg.ProfileID) and executes service.outlook_mail with the
// given config, in-process — shared by compose/send-draft/reject-draft so
// none of them duplicate credential resolution or node dispatch.
func runOutlookNode(cmd *cobra.Command, cfg *globalConfig, db *sql.DB, connectionID string, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	if connectionID == "" {
		connectionID = "outlook"
	}
	registry := buildNodeRegistry(cfg.Verbose, db)
	factory, ok := registry.Get("service.outlook_mail")
	if !ok {
		return nil, fmt.Errorf("service.outlook_mail node not registered")
	}

	connStore := connections.NewStore(db)
	credData, err := resolveCredentialData(cmd.Context(), connStore, connectionID, cfg.ProfileID)
	if err != nil {
		return nil, fmt.Errorf("resolving connection %q: %w", connectionID, err)
	}
	for k, v := range credData {
		config[k] = v
	}

	outputs, err := factory().Execute(cmd.Context(), workflow.NodeInput{}, config)
	if err != nil {
		op, _ := config["operation"].(string)
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	return outputs, nil
}
