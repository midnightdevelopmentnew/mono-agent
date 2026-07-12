package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"monoagent/internal/workflow"
)

// OutlookMailNode implements the service.outlook_mail node type, talking to
// Outlook/Hotmail over the Microsoft Graph REST API using an OAuth access
// token. Unlike comm.outlook_read/outlook_send (raw IMAP/SMTP), this does not
// need XOAUTH2 and keeps working now that Microsoft has deprecated Basic Auth
// for IMAP on outlook.com/hotmail.com accounts.
type OutlookMailNode struct{}

func (n *OutlookMailNode) Type() string { return "service.outlook_mail" }

const outlookGraphBaseURL = "https://graph.microsoft.com/v1.0/me"

func (n *OutlookMailNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	accessToken := strVal(config, "access_token")
	if accessToken == "" {
		return nil, fmt.Errorf("outlook_mail: access_token is required")
	}
	operation := strVal(config, "operation")
	maxResults := intVal(config, "max_results")
	if maxResults == 0 {
		maxResults = 10
	}

	var items []workflow.Item

	switch operation {
	case "send_message":
		to := strVal(config, "to")
		subject := strVal(config, "subject")
		body := strVal(config, "body")
		bodyType := strVal(config, "body_type")
		if bodyType == "" {
			bodyType = "Text"
		} else if bodyType == "html" {
			bodyType = "HTML"
		} else {
			bodyType = "Text"
		}
		sendBody := map[string]interface{}{
			"message": map[string]interface{}{
				"subject": subject,
				"body": map[string]interface{}{
					"contentType": bodyType,
					"content":     body,
				},
				"toRecipients": []map[string]interface{}{
					{"emailAddress": map[string]interface{}{"address": to}},
				},
			},
		}
		if _, err := outlookGraphRequest(ctx, "POST", outlookGraphBaseURL+"/sendMail", accessToken, sendBody); err != nil {
			return nil, fmt.Errorf("outlook_mail send_message: %w", err)
		}
		items = []workflow.Item{workflow.NewItem(map[string]interface{}{"status": "sent", "to": to, "subject": subject})}

	case "create_draft":
		to := strVal(config, "to")
		subject := strVal(config, "subject")
		body := strVal(config, "body")
		bodyType := strVal(config, "body_type")
		if bodyType == "html" {
			bodyType = "HTML"
		} else {
			bodyType = "Text"
		}
		draftBody := map[string]interface{}{
			"subject": subject,
			"body": map[string]interface{}{
				"contentType": bodyType,
				"content":     body,
			},
		}
		if to != "" {
			draftBody["toRecipients"] = []map[string]interface{}{
				{"emailAddress": map[string]interface{}{"address": to}},
			}
		}
		// POST /me/messages (unlike /sendMail) saves the message to Drafts
		// without sending it.
		data, err := outlookGraphRequest(ctx, "POST", outlookGraphBaseURL+"/messages", accessToken, draftBody)
		if err != nil {
			return nil, fmt.Errorf("outlook_mail create_draft: %w", err)
		}
		items = []workflow.Item{workflow.NewItem(data)}

	case "send_draft":
		messageID := strVal(config, "message_id")
		if messageID == "" {
			return nil, fmt.Errorf("outlook_mail: message_id is required for send_draft")
		}
		// POST /messages/{id}/send sends an existing draft as-is.
		if _, err := outlookGraphRequest(ctx, "POST", outlookGraphBaseURL+"/messages/"+messageID+"/send", accessToken, map[string]interface{}{}); err != nil {
			return nil, fmt.Errorf("outlook_mail send_draft: %w", err)
		}
		items = []workflow.Item{workflow.NewItem(map[string]interface{}{"status": "sent", "message_id": messageID})}

	case "delete_message":
		messageID := strVal(config, "message_id")
		if messageID == "" {
			return nil, fmt.Errorf("outlook_mail: message_id is required for delete_message")
		}
		if _, err := outlookGraphRequest(ctx, "DELETE", outlookGraphBaseURL+"/messages/"+messageID, accessToken, nil); err != nil {
			return nil, fmt.Errorf("outlook_mail delete_message: %w", err)
		}
		items = []workflow.Item{workflow.NewItem(map[string]interface{}{"status": "deleted", "message_id": messageID})}

	case "list_messages":
		mailbox := strVal(config, "mailbox")
		if mailbox == "" {
			mailbox = "inbox"
		}
		url := fmt.Sprintf("%s/mailFolders/%s/messages?$top=%d&$select=id,subject,from,toRecipients,receivedDateTime,body,bodyPreview,isRead", outlookGraphBaseURL, mailbox, maxResults)
		// $search (full-text over subject/body/sender, or a field-scoped query
		// like `from:someone@x.com` or `subject:invoice`) and $filter are
		// mutually exclusive in the same Graph request, so prefer $search when
		// given — it covers the more common "find this email" case directly.
		if search := strVal(config, "search"); search != "" {
			url += "&$search=" + gmailURLEncode(`"`+search+`"`)
		} else {
			var filters []string
			if unreadOnly, _ := config["unread_only"].(bool); unreadOnly {
				filters = append(filters, "isRead eq false")
			}
			if from := strVal(config, "from_address"); from != "" {
				filters = append(filters, fmt.Sprintf("from/emailAddress/address eq '%s'", from))
			}
			if len(filters) > 0 {
				url += "&$filter=" + gmailURLEncode(strings.Join(filters, " and "))
			}
		}
		data, err := outlookGraphRequest(ctx, "GET", url, accessToken, nil)
		if err != nil {
			return nil, fmt.Errorf("outlook_mail list_messages: %w", err)
		}
		messages, _ := data["value"].([]interface{})
		items = make([]workflow.Item, 0, len(messages))
		for _, m := range messages {
			if msg, ok := m.(map[string]interface{}); ok {
				items = append(items, workflow.NewItem(msg))
			}
		}

	case "whoami":
		// Resolves the authenticated account's own address using the
		// smallest possible read: one field ("from" or "toRecipients") of
		// at most one message, no subject/body/content fetched. Tries
		// Sent Items first (its "from" is always the account owner); falls
		// back to Inbox ("toRecipients" — mail addressed to the owner) for
		// mailboxes that have never sent anything.
		address, source, err := outlookWhoAmI(ctx, accessToken)
		if err != nil {
			return nil, fmt.Errorf("outlook_mail whoami: %w", err)
		}
		items = []workflow.Item{workflow.NewItem(map[string]interface{}{"address": address, "source": source})}

	case "get_message":
		messageID := strVal(config, "message_id")
		if messageID == "" {
			return nil, fmt.Errorf("outlook_mail: message_id is required for get_message")
		}
		data, err := outlookGraphRequest(ctx, "GET", outlookGraphBaseURL+"/messages/"+messageID, accessToken, nil)
		if err != nil {
			return nil, fmt.Errorf("outlook_mail get_message: %w", err)
		}
		items = []workflow.Item{workflow.NewItem(data)}

	default:
		return nil, fmt.Errorf("outlook_mail: unknown operation %q", operation)
	}

	return []workflow.NodeOutput{{Handle: "main", Items: items}}, nil
}

// outlookWhoAmI resolves the mailbox owner's own address without the
// User.Read scope (not part of this app's OAuth scopes, and adding it would
// force every existing connection to be reconnected). Mail.Read/ReadWrite
// alone can't reach /me, so this reads the single smallest field that
// reveals the owner's identity from mail data instead.
func outlookWhoAmI(ctx context.Context, accessToken string) (address, source string, err error) {
	// A Sent Items message's "from" is always the account owner.
	data, err := outlookGraphRequest(ctx, "GET",
		outlookGraphBaseURL+"/mailFolders/sentitems/messages?$top=1&$select=from", accessToken, nil)
	if err == nil {
		if addr := firstEmailAddress(data, "from"); addr != "" {
			return addr, "sentitems", nil
		}
	}
	// Fall back to Inbox: mail addressed to the owner names them in toRecipients.
	data, err = outlookGraphRequest(ctx, "GET",
		outlookGraphBaseURL+"/mailFolders/inbox/messages?$top=1&$select=toRecipients", accessToken, nil)
	if err != nil {
		return "", "", err
	}
	if addr := firstEmailAddress(data, "toRecipients"); addr != "" {
		return addr, "inbox", nil
	}
	return "", "", fmt.Errorf("mailbox has no sent or received messages to resolve identity from")
}

// firstEmailAddress extracts the address from the first message's given
// field, which is either a single {"emailAddress":{...}} object ("from") or
// an array of them ("toRecipients").
func firstEmailAddress(data map[string]interface{}, field string) string {
	values, _ := data["value"].([]interface{})
	if len(values) == 0 {
		return ""
	}
	msg, _ := values[0].(map[string]interface{})
	switch field {
	case "from":
		fromObj, _ := msg["from"].(map[string]interface{})
		ea, _ := fromObj["emailAddress"].(map[string]interface{})
		addr, _ := ea["address"].(string)
		return addr
	case "toRecipients":
		recipients, _ := msg["toRecipients"].([]interface{})
		if len(recipients) == 0 {
			return ""
		}
		rm, _ := recipients[0].(map[string]interface{})
		ea, _ := rm["emailAddress"].(map[string]interface{})
		addr, _ := ea["address"].(string)
		return addr
	}
	return ""
}

// outlookGraphRequest makes an authenticated request to the Microsoft Graph API.
func outlookGraphRequest(ctx context.Context, method, url, accessToken string, body interface{}) (map[string]interface{}, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("outlook_mail: marshaling body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("outlook_mail: creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("outlook_mail %s %s: %w", method, url, err)
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("outlook_mail: reading response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("outlook_mail HTTP %d: %s", resp.StatusCode, string(respBytes))
	}
	if len(respBytes) == 0 {
		return map[string]interface{}{}, nil
	}
	var result map[string]interface{}
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return nil, fmt.Errorf("outlook_mail: parsing JSON: %w", err)
	}
	return result, nil
}
