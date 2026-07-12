package comm

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"strings"
	"time"

	"monoagent/internal/workflow"
)

// OutlookReadNode fetches emails from Outlook / Hotmail via IMAP.
// Type: "comm.outlook_read"
//
// Config fields:
//
//	"email"       (string, required): Outlook/Hotmail address (also the IMAP username)
//	"password"    (string, required): Account password or app password
//	"mailbox"     (string, default "INBOX"): mailbox folder to read
//	"limit"       (int, default 10): max messages to fetch (most recent first)
//	"unread_only" (bool, default false): only return unseen messages
//
// Uses Outlook IMAP: outlook.office365.com:993 with TLS.
// Returns each message as an Item with fields:
// "subject", "from", "date", "body", "message_id", "read"
type OutlookReadNode struct{}

func (n *OutlookReadNode) Type() string { return "comm.outlook_read" }

func (n *OutlookReadNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	const imapHost = "outlook.office365.com"
	const imapPort = 993

	email, _ := config["email"].(string)
	if email == "" {
		return nil, fmt.Errorf("comm.outlook_read: email is required")
	}

	password, _ := config["password"].(string)
	if password == "" {
		password, _ = config["app_password"].(string)
	}
	if password == "" {
		return nil, fmt.Errorf("comm.outlook_read: password is required")
	}

	mailbox := "INBOX"
	if mb, ok := config["mailbox"].(string); ok && mb != "" {
		mailbox = mb
	}

	limit := 10
	switch v := config["limit"].(type) {
	case int:
		limit = v
	case float64:
		limit = int(v)
	}
	if limit <= 0 {
		limit = 10
	}

	unreadOnly := false
	if u, ok := config["unread_only"].(bool); ok {
		unreadOnly = u
	}

	items, err := fetchOutlookMail(ctx, imapHost, imapPort, email, password, mailbox, limit, unreadOnly)
	if err != nil {
		return nil, fmt.Errorf("comm.outlook_read: %w", err)
	}

	out := make([]workflow.Item, len(items))
	for i, m := range items {
		out[i] = workflow.NewItem(m)
	}
	return []workflow.NodeOutput{{Handle: "main", Items: out}}, nil
}

// fetchOutlookMail performs a minimal IMAP fetch using raw text protocol.
// This avoids a third-party IMAP library dependency while still being functional
// for simple cases. For production use, replace with go-imap or similar.
func fetchOutlookMail(ctx context.Context, host string, port int, username, password, mailbox string, limit int, unreadOnly bool) ([]map[string]interface{}, error) {
	addr := fmt.Sprintf("%s:%d", host, port)

	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 15 * time.Second},
		Config:    &tls.Config{ServerName: host},
	}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}

	tc := textproto.NewConn(conn)
	defer tc.Close() // also closes the underlying conn

	// Close the connection when the workflow context is cancelled so blocked
	// reads unblock instead of hanging the node goroutine indefinitely.
	ctxDone := make(chan struct{})
	defer close(ctxDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-ctxDone:
		}
	}()

	readLine := func() (string, error) {
		// Idle read deadline: a stalled server (e.g. terminating tag never
		// arrives) fails the read instead of blocking forever.
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		line, err := tc.ReadLine()
		return line, err
	}
	send := func(tag, cmd string) error {
		_, err := fmt.Fprintf(conn, "%s %s\r\n", tag, cmd)
		return err
	}
	expectOK := func(tag string) error {
		for {
			line, err := readLine()
			if err != nil {
				return err
			}
			if strings.HasPrefix(line, tag+" OK") {
				return nil
			}
			if strings.HasPrefix(line, tag+" NO") || strings.HasPrefix(line, tag+" BAD") {
				return fmt.Errorf("IMAP error: %s", line)
			}
		}
	}

	// Read server greeting
	if _, err := readLine(); err != nil {
		return nil, fmt.Errorf("greeting: %w", err)
	}

	// LOGIN
	if err := send("A1", fmt.Sprintf("LOGIN %q %q", username, password)); err != nil {
		return nil, fmt.Errorf("login send: %w", err)
	}
	if err := expectOK("A1"); err != nil {
		return nil, fmt.Errorf("login: %w", err)
	}

	// SELECT mailbox
	if err := send("A2", fmt.Sprintf("SELECT %q", mailbox)); err != nil {
		return nil, err
	}
	var totalMessages int
	for {
		line, err := readLine()
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(line, "A2 OK") {
			break
		}
		if strings.HasPrefix(line, "A2 NO") || strings.HasPrefix(line, "A2 BAD") {
			return nil, fmt.Errorf("SELECT: %s", line)
		}
		// Parse EXISTS count
		var n int
		if _, err := fmt.Sscanf(line, "* %d EXISTS", &n); err == nil {
			totalMessages = n
		}
	}

	if totalMessages == 0 {
		return []map[string]interface{}{}, nil
	}

	// Determine message range (most recent first)
	start := totalMessages - limit + 1
	if start < 1 {
		start = 1
	}
	searchSpec := fmt.Sprintf("%d:%d", start, totalMessages)
	if unreadOnly {
		// For unread-only, search all UNSEEN — simplified: fetch range and filter by \Seen flag
		searchSpec = fmt.Sprintf("%d:*", start)
	}

	// FETCH envelope and body
	fetchCmd := fmt.Sprintf("FETCH %s (FLAGS ENVELOPE BODY[TEXT]<0.2048>)", searchSpec)
	if err := send("A3", fetchCmd); err != nil {
		return nil, err
	}

	var results []map[string]interface{}
	var current map[string]interface{}
	var inFetch bool

	for {
		line, err := readLine()
		if err != nil {
			break
		}
		if strings.HasPrefix(line, "A3 OK") {
			break
		}
		if strings.HasPrefix(line, "A3 NO") || strings.HasPrefix(line, "A3 BAD") {
			return nil, fmt.Errorf("FETCH: %s", line)
		}

		// New message fetch response
		if strings.Contains(line, "FETCH (") {
			current = map[string]interface{}{}
			inFetch = true
		}

		if !inFetch || current == nil {
			continue
		}

		// Flags
		if strings.Contains(line, "FLAGS (") {
			flagStart := strings.Index(line, "FLAGS (") + 7
			end := strings.Index(line[flagStart:], ")")
			if end >= 0 {
				flags := line[flagStart : flagStart+end]
				isRead := strings.Contains(flags, "\\Seen")
				current["read"] = isRead
				if unreadOnly && isRead {
					current = nil
					inFetch = false
				}
			}
		}

		// ENVELOPE: (date subject from sender reply-to to cc bcc in-reply-to message-id)
		if strings.Contains(line, "ENVELOPE (") {
			ei := strings.Index(line, "ENVELOPE (") + 10
			env := extractParenContent(line[ei:])
			parts := parseEnvelopeParts(env)
			if len(parts) >= 10 {
				current["date"] = unquoteIMAPString(parts[0])
				current["subject"] = decodeIMAPSubject(unquoteIMAPString(parts[1]))
				current["from"] = parseIMAPAddress(parts[2])
				current["message_id"] = unquoteIMAPString(parts[9])
			}
		}

		// Body text — IMAP literal: "BODY[TEXT] {N}" followed by N bytes of content.
		if strings.Contains(line, "BODY[TEXT]") {
			// Extract byte count from {N} at end of line.
			var bodySize int
			if bi := strings.LastIndex(line, "{"); bi >= 0 {
				fmt.Sscanf(line[bi:], "{%d}", &bodySize)
			}
			if bodySize > 0 {
				buf := make([]byte, bodySize)
				_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
				if _, rerr := io.ReadFull(tc.R, buf); rerr == nil {
					bodyText := string(buf)
					if len(bodyText) > 2048 {
						bodyText = bodyText[:2048] + "…"
					}
					current["body"] = strings.TrimSpace(bodyText)
				}
				// Consume the trailing \r\n after the literal.
				_, _ = readLine()
			} else {
				// Fallback: inline value after BODY[TEXT] on same line
				if idx := strings.Index(line, "BODY[TEXT] "); idx >= 0 {
					current["body"] = strings.TrimSpace(line[idx+11:])
				}
			}
		}

		// End of this message
		if line == ")" {
			if current != nil && len(current) > 0 {
				if current["read"] == nil {
					current["read"] = false
				}
				results = append(results, current)
			}
			current = nil
			inFetch = false
		}
	}

	// Reverse so newest is first
	for i, j := 0, len(results)-1; i < j; i, j = i+1, j-1 {
		results[i], results[j] = results[j], results[i]
	}

	// LOGOUT
	_ = send("A4", "LOGOUT")

	return results, nil
}

// extractParenContent returns everything inside the first balanced set of parentheses.
func extractParenContent(s string) string {
	depth := 0
	start := -1
	for i, ch := range s {
		if ch == '(' {
			if depth == 0 {
				start = i + 1
			}
			depth++
		} else if ch == ')' {
			depth--
			if depth == 0 && start >= 0 {
				return s[start:i]
			}
		}
	}
	return s
}

// parseEnvelopeParts splits an IMAP envelope string into its 10 fields.
// This is a simplified parser that handles quoted strings and nested parens.
func parseEnvelopeParts(s string) []string {
	var parts []string
	i := 0
	for i < len(s) {
		ch := s[i]
		if ch == ' ' {
			i++
			continue
		}
		if ch == '"' {
			// Quoted string
			j := i + 1
			for j < len(s) && s[j] != '"' {
				if s[j] == '\\' {
					j++
				}
				j++
			}
			parts = append(parts, s[i:j+1])
			i = j + 1
		} else if ch == '(' {
			// Nested paren group
			depth := 0
			j := i
			for j < len(s) {
				if s[j] == '(' {
					depth++
				} else if s[j] == ')' {
					depth--
					if depth == 0 {
						break
					}
				}
				j++
			}
			parts = append(parts, s[i:j+1])
			i = j + 1
		} else {
			// NIL or unquoted atom
			j := i
			for j < len(s) && s[j] != ' ' && s[j] != ')' {
				j++
			}
			parts = append(parts, s[i:j])
			i = j
		}
	}
	return parts
}

func unquoteIMAPString(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return strings.ReplaceAll(s[1:len(s)-1], "\\\"", "\"")
	}
	if s == "NIL" {
		return ""
	}
	return s
}

// decodeIMAPSubject handles simple ASCII subjects (=?charset?encoding?text?= decoding is omitted).
func decodeIMAPSubject(s string) string {
	return s
}

// parseIMAPAddress extracts a display address from an IMAP address list like ((name NIL mailbox host)).
func parseIMAPAddress(s string) string {
	inner := extractParenContent(s)
	if inner == "" {
		return ""
	}
	inner = extractParenContent(inner)
	parts := parseEnvelopeParts(inner)
	if len(parts) < 4 {
		return ""
	}
	name := unquoteIMAPString(parts[0])
	user := unquoteIMAPString(parts[2])
	host := unquoteIMAPString(parts[3])
	addr := ""
	if user != "" && host != "" {
		addr = user + "@" + host
	}
	if name != "" && addr != "" {
		return name + " <" + addr + ">"
	}
	return addr
}
