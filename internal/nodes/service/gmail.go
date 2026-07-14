package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"monoagent/internal/workflow"
)

// GmailNode implements the service.gmail node type.
//
// Operations:
//
//	send_message: "to"/"cc"/"bcc" (string, comma/semicolon-separated for
//	  multiple), "subject", "body", "body_type" ("text"/"html")
//	create_draft: same fields as send_message, saves to Drafts instead
//	send_draft: "message_id" — the DRAFT id (create_draft's top-level "id"),
//	  not the underlying message's own id, which is a distinct value in
//	  Gmail's API
//	create_reply/reply: "message_id" (the message being replied to),
//	  "reply_all" (bool), "body", "from" — recipients, subject, threadId, and
//	  In-Reply-To/References headers are derived from the original message;
//	  the body is used as-is (Gmail's API has no auto-quote of the original
//	  like Graph's createReply)
//	list_messages/get_message/list_labels/trash_message: as before
type GmailNode struct{}

func (n *GmailNode) Type() string { return "service.gmail" }

const gmailBaseURL = "https://gmail.googleapis.com/gmail/v1/users/me"

func (n *GmailNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	accessToken := strVal(config, "access_token")
	if accessToken == "" {
		return nil, fmt.Errorf("gmail: access_token is required")
	}
	operation := strVal(config, "operation")
	maxResults := intVal(config, "max_results")
	if maxResults == 0 {
		maxResults = 10
	}

	var items []workflow.Item

	switch operation {
	case "send_message":
		opts := gmailMessageOptsFromConfig(config)
		raw, err := gmailBuildRFC2822(opts)
		if err != nil {
			return nil, fmt.Errorf("gmail: building message: %w", err)
		}
		sendBody := map[string]interface{}{"raw": raw}
		resp, err := gmailRequest(ctx, "POST", gmailBaseURL+"/messages/send", accessToken, sendBody)
		if err != nil {
			return nil, fmt.Errorf("gmail send_message: %w", err)
		}
		items = []workflow.Item{workflow.NewItem(resp)}

	case "create_draft":
		opts := gmailMessageOptsFromConfig(config)
		raw, err := gmailBuildRFC2822(opts)
		if err != nil {
			return nil, fmt.Errorf("gmail: building message: %w", err)
		}
		draftBody := map[string]interface{}{"message": map[string]interface{}{"raw": raw}}
		data, err := gmailRequest(ctx, "POST", gmailBaseURL+"/drafts", accessToken, draftBody)
		if err != nil {
			return nil, fmt.Errorf("gmail create_draft: %w", err)
		}
		items = []workflow.Item{workflow.NewItem(data)}

	case "send_draft":
		// The Gmail API's draft id (the "id" field on create_draft's response)
		// is a different value from the underlying message's own id — pass
		// the draft id here, not message.id.
		draftID := strVal(config, "message_id")
		if draftID == "" {
			return nil, fmt.Errorf("gmail: message_id (the draft id) is required for send_draft")
		}
		data, err := gmailRequest(ctx, "POST", gmailBaseURL+"/drafts/send", accessToken, map[string]interface{}{"id": draftID})
		if err != nil {
			return nil, fmt.Errorf("gmail send_draft: %w", err)
		}
		items = []workflow.Item{workflow.NewItem(data)}

	case "create_reply", "reply":
		// Gmail's API has no one-call "createReply" helper like Graph's, so
		// this reconstructs threading manually: fetch the original message's
		// Message-ID/References/Subject/From/To/Cc headers and threadId, then
		// build a new RFC2822 message with In-Reply-To/References set to the
		// original and the same threadId, which is what keeps Gmail's UI (and
		// any RFC2822-aware client) grouping these into one conversation.
		messageID := strVal(config, "message_id")
		if messageID == "" {
			return nil, fmt.Errorf("gmail: message_id is required for %s", operation)
		}
		orig, err := gmailFetchMessageMetadata(ctx, accessToken, messageID)
		if err != nil {
			return nil, fmt.Errorf("gmail %s: fetching original message: %w", operation, err)
		}
		payload, _ := orig["payload"].(map[string]interface{})
		threadID, _ := orig["threadId"].(string)
		origMessageIDHeader := gmailHeaderValue(payload, "Message-ID")
		origReferences := gmailHeaderValue(payload, "References")
		origSubject := gmailHeaderValue(payload, "Subject")
		origFrom := gmailHeaderValue(payload, "From")
		origTo := gmailHeaderValue(payload, "To")
		origCc := gmailHeaderValue(payload, "Cc")

		replyAll := boolVal(config, "reply_all")
		// Best-effort: excludes the account's own address from a reply-all's
		// recipients. If this fails (e.g. a scope issue), reply-all still
		// works, it just also lists the account itself as a recipient.
		selfAddr, _ := gmailWhoAmI(ctx, accessToken)

		toEntries := gmailSplitAddressList(origFrom)
		var ccEntries []string
		if replyAll {
			for _, e := range gmailSplitAddressList(origTo) {
				if !strings.EqualFold(gmailBareAddress(e), selfAddr) {
					toEntries = append(toEntries, e)
				}
			}
			for _, e := range gmailSplitAddressList(origCc) {
				if !strings.EqualFold(gmailBareAddress(e), selfAddr) {
					ccEntries = append(ccEntries, e)
				}
			}
		}

		references := origMessageIDHeader
		if origReferences != "" {
			references = origReferences + " " + origMessageIDHeader
		}

		opts := gmailMessageOpts{
			From:       strVal(config, "from"),
			To:         strings.Join(toEntries, ", "),
			Cc:         strings.Join(ccEntries, ", "),
			Subject:    prefixReplySubjectGmail(origSubject),
			Body:       strVal(config, "body"),
			BodyType:   strVal(config, "body_type"),
			InReplyTo:  origMessageIDHeader,
			References: references,
		}
		raw, err := gmailBuildRFC2822(opts)
		if err != nil {
			return nil, fmt.Errorf("gmail %s: building message: %w", operation, err)
		}

		if operation == "create_reply" {
			draftBody := map[string]interface{}{"message": map[string]interface{}{"raw": raw, "threadId": threadID}}
			data, err := gmailRequest(ctx, "POST", gmailBaseURL+"/drafts", accessToken, draftBody)
			if err != nil {
				return nil, fmt.Errorf("gmail create_reply: %w", err)
			}
			items = []workflow.Item{workflow.NewItem(data)}
		} else {
			data, err := gmailRequest(ctx, "POST", gmailBaseURL+"/messages/send", accessToken, map[string]interface{}{"raw": raw, "threadId": threadID})
			if err != nil {
				return nil, fmt.Errorf("gmail reply: %w", err)
			}
			items = []workflow.Item{workflow.NewItem(data)}
		}

	case "list_messages":
		url := fmt.Sprintf("%s/messages?maxResults=%d", gmailBaseURL, maxResults)
		if q := strVal(config, "query"); q != "" {
			url += "&q=" + gmailURLEncode(q)
		}
		labelIDs := strSliceVal(config, "label_ids")
		for _, lid := range labelIDs {
			url += "&labelIds=" + lid
		}
		data, err := gmailRequest(ctx, "GET", url, accessToken, nil)
		if err != nil {
			return nil, fmt.Errorf("gmail list_messages: %w", err)
		}
		messages, _ := data["messages"].([]interface{})
		items = make([]workflow.Item, 0, len(messages))
		for _, m := range messages {
			if msg, ok := m.(map[string]interface{}); ok {
				items = append(items, workflow.NewItem(msg))
			}
		}

	case "get_message":
		messageID := strVal(config, "message_id")
		if messageID == "" {
			return nil, fmt.Errorf("gmail: message_id is required for get_message")
		}
		url := gmailBaseURL + "/messages/" + messageID
		data, err := gmailRequest(ctx, "GET", url, accessToken, nil)
		if err != nil {
			return nil, fmt.Errorf("gmail get_message: %w", err)
		}
		items = []workflow.Item{workflow.NewItem(data)}

	case "list_labels":
		data, err := gmailRequest(ctx, "GET", gmailBaseURL+"/labels", accessToken, nil)
		if err != nil {
			return nil, fmt.Errorf("gmail list_labels: %w", err)
		}
		labels, _ := data["labels"].([]interface{})
		items = make([]workflow.Item, 0, len(labels))
		for _, l := range labels {
			if label, ok := l.(map[string]interface{}); ok {
				items = append(items, workflow.NewItem(label))
			}
		}

	case "trash_message":
		messageID := strVal(config, "message_id")
		if messageID == "" {
			return nil, fmt.Errorf("gmail: message_id is required for trash_message")
		}
		url := gmailBaseURL + "/messages/" + messageID + "/trash"
		data, err := gmailRequest(ctx, "POST", url, accessToken, nil)
		if err != nil {
			return nil, fmt.Errorf("gmail trash_message: %w", err)
		}
		items = []workflow.Item{workflow.NewItem(data)}

	default:
		return nil, fmt.Errorf("gmail: unknown operation %q", operation)
	}

	return []workflow.NodeOutput{{Handle: "main", Items: items}}, nil
}

// gmailRequest makes an authenticated request to the Gmail API.
func gmailRequest(ctx context.Context, method, url, accessToken string, body interface{}) (map[string]interface{}, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("gmail: marshaling body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("gmail: creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gmail %s %s: %w", method, url, err)
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("gmail: reading response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("gmail HTTP %d: %s", resp.StatusCode, string(respBytes))
	}
	if len(respBytes) == 0 {
		return map[string]interface{}{}, nil
	}
	var result map[string]interface{}
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return nil, fmt.Errorf("gmail: parsing JSON: %w", err)
	}
	return result, nil
}

// gmailMessageOpts holds the fields needed to build an RFC2822 message.
type gmailMessageOpts struct {
	From, To, Cc, Bcc, Subject, Body, BodyType string
	InReplyTo, References                      string
}

// gmailMessageOptsFromConfig reads the common send_message/create_draft
// config fields. "to" is comma/semicolon-separated for multiple recipients —
// Gmail parses that directly out of a single To: header line, unlike
// Graph's JSON recipient arrays, so no splitting is needed here.
func gmailMessageOptsFromConfig(config map[string]interface{}) gmailMessageOpts {
	bodyType := strVal(config, "body_type")
	if bodyType == "" {
		bodyType = "text"
	}
	return gmailMessageOpts{
		From:     strVal(config, "from"),
		To:       strVal(config, "to"),
		Cc:       strVal(config, "cc"),
		Bcc:      strVal(config, "bcc"),
		Subject:  strVal(config, "subject"),
		Body:     strVal(config, "body"),
		BodyType: bodyType,
	}
}

// gmailBuildRFC2822 constructs an RFC 2822 email message and returns it as a base64url-encoded string.
func gmailBuildRFC2822(opts gmailMessageOpts) (string, error) {
	contentType := "text/plain"
	if opts.BodyType == "html" {
		contentType = "text/html"
	}

	// Strip CR/LF from header values to prevent header injection: a value like
	// "Invoice\r\nBcc: attacker@evil.com" would otherwise inject extra headers.
	from := gmailSanitizeHeader(opts.From)
	to := gmailSanitizeHeader(opts.To)
	cc := gmailSanitizeHeader(opts.Cc)
	bcc := gmailSanitizeHeader(opts.Bcc)
	subject := gmailSanitizeHeader(opts.Subject)
	inReplyTo := gmailSanitizeHeader(opts.InReplyTo)
	references := gmailSanitizeHeader(opts.References)

	var sb strings.Builder
	if from != "" {
		sb.WriteString("From: " + from + "\r\n")
	}
	if to != "" {
		sb.WriteString("To: " + to + "\r\n")
	}
	if cc != "" {
		sb.WriteString("Cc: " + cc + "\r\n")
	}
	if bcc != "" {
		sb.WriteString("Bcc: " + bcc + "\r\n")
	}
	if subject != "" {
		sb.WriteString("Subject: " + subject + "\r\n")
	}
	if inReplyTo != "" {
		sb.WriteString("In-Reply-To: " + inReplyTo + "\r\n")
	}
	if references != "" {
		sb.WriteString("References: " + references + "\r\n")
	}
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString("Content-Type: " + contentType + "; charset=\"UTF-8\"\r\n")
	sb.WriteString("\r\n")
	sb.WriteString(opts.Body)

	encoded := base64.URLEncoding.EncodeToString([]byte(sb.String()))
	return encoded, nil
}

// gmailSanitizeHeader removes CR and LF characters from an email header value
// to prevent header injection (e.g. a smuggled "Bcc:" line).
func gmailSanitizeHeader(s string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(s)
}

// gmailURLEncode encodes a string for use in a URL query parameter.
func gmailURLEncode(s string) string {
	return url.QueryEscape(s)
}

// prefixReplySubjectGmail adds a "Re: " prefix unless the subject already has
// one, mirroring standard mail client behavior for replies.
func prefixReplySubjectGmail(subject string) string {
	if subject == "" || strings.HasPrefix(strings.ToLower(subject), "re:") {
		return subject
	}
	return "Re: " + subject
}

// gmailFetchMessageMetadata fetches just the headers needed to reply to a
// message (Message-ID/References/Subject/From/To/Cc) plus its threadId,
// without pulling the full body.
func gmailFetchMessageMetadata(ctx context.Context, accessToken, messageID string) (map[string]interface{}, error) {
	v := url.Values{}
	v.Set("format", "metadata")
	for _, h := range []string{"Message-ID", "References", "Subject", "From", "To", "Cc"} {
		v.Add("metadataHeaders", h)
	}
	return gmailRequest(ctx, "GET", gmailBaseURL+"/messages/"+messageID+"?"+v.Encode(), accessToken, nil)
}

// gmailHeaderValue looks up a header by name (case-insensitive) in a
// message's payload.headers array.
func gmailHeaderValue(payload map[string]interface{}, name string) string {
	headers, _ := payload["headers"].([]interface{})
	for _, h := range headers {
		hm, ok := h.(map[string]interface{})
		if !ok {
			continue
		}
		n, _ := hm["name"].(string)
		if strings.EqualFold(n, name) {
			v, _ := hm["value"].(string)
			return v
		}
	}
	return ""
}

// gmailWhoAmI resolves the authenticated account's own address, used to
// exclude the account itself from a reply-all's recipients.
func gmailWhoAmI(ctx context.Context, accessToken string) (string, error) {
	data, err := gmailRequest(ctx, "GET", gmailBaseURL+"/profile", accessToken, nil)
	if err != nil {
		return "", err
	}
	addr, _ := data["emailAddress"].(string)
	return addr, nil
}

// gmailSplitAddressList splits a comma-separated address header value (e.g.
// "A Name <a@x.com>, b@x.com") into individual entries, preserving each
// entry's original display form for reuse in a new header. Note: a display
// name containing a literal comma (rare, and only valid RFC5322 if quoted)
// isn't handled — this is a plain split, not a full address parser.
func gmailSplitAddressList(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// gmailBareAddress extracts the bare address from a "Name <addr>" or "addr" entry.
func gmailBareAddress(entry string) string {
	if i := strings.LastIndex(entry, "<"); i >= 0 {
		if j := strings.Index(entry[i:], ">"); j >= 0 {
			return strings.TrimSpace(entry[i+1 : i+j])
		}
	}
	return strings.TrimSpace(entry)
}
