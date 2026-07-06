package comm

import (
	"context"
	"fmt"
	"net"
	"net/smtp"

	"monoagent/internal/workflow"
)

// OutlookSendNode sends an email via Outlook / Hotmail SMTP.
// Type: "comm.outlook_send"
//
// Config fields:
//
//	"email"       (string, required): Outlook/Hotmail sender address (also the SMTP username)
//	"password"    (string, required): Account password or app password
//	"to"          (string or []string, required): recipient(s)
//	"cc"          (string or []string): CC recipients
//	"bcc"         (string or []string): BCC recipients
//	"subject"     (string, required): email subject
//	"body"        (string, required): email body
//	"body_type"   (string): "text" (default) or "html"
//	"attachments" ([]string): file paths to attach
//
// Uses Outlook SMTP: smtp-mail.outlook.com:587 with STARTTLS.
type OutlookSendNode struct{}

func (n *OutlookSendNode) Type() string { return "comm.outlook_send" }

func (n *OutlookSendNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	const smtpHost = "smtp-mail.outlook.com"
	const smtpPort = 587

	email, _ := config["email"].(string)
	if email == "" {
		return nil, fmt.Errorf("comm.outlook_send: email is required")
	}

	// Support both "password" and "app_password" keys (connection saves as app_password).
	password, _ := config["password"].(string)
	if password == "" {
		password, _ = config["app_password"].(string)
	}
	if password == "" {
		return nil, fmt.Errorf("comm.outlook_send: password is required")
	}

	toAddrs := toStringSlice(config["to"])
	if len(toAddrs) == 0 {
		return nil, fmt.Errorf("comm.outlook_send: to is required")
	}

	ccAddrs := toStringSlice(config["cc"])
	bccAddrs := toStringSlice(config["bcc"])

	subject, _ := config["subject"].(string)
	if subject == "" {
		return nil, fmt.Errorf("comm.outlook_send: subject is required")
	}

	body, _ := config["body"].(string)
	if body == "" {
		return nil, fmt.Errorf("comm.outlook_send: body is required")
	}

	bodyType := "text"
	if bt, ok := config["body_type"].(string); ok && bt != "" {
		bodyType = bt
	}

	var attachmentPaths []string
	if ap, ok := config["attachments"]; ok {
		switch v := ap.(type) {
		case []string:
			attachmentPaths = v
		case []interface{}:
			for _, a := range v {
				if s, ok := a.(string); ok {
					attachmentPaths = append(attachmentPaths, s)
				}
			}
		}
	}

	allRecipients := make([]string, 0, len(toAddrs)+len(ccAddrs)+len(bccAddrs))
	allRecipients = append(allRecipients, toAddrs...)
	allRecipients = append(allRecipients, ccAddrs...)
	allRecipients = append(allRecipients, bccAddrs...)

	msgBytes, err := buildMIMEMessage(email, toAddrs, ccAddrs, subject, body, bodyType, attachmentPaths)
	if err != nil {
		return nil, fmt.Errorf("comm.outlook_send: build message: %w", err)
	}

	addr := net.JoinHostPort(smtpHost, fmt.Sprintf("%d", smtpPort))
	auth := smtp.PlainAuth("", email, password, smtpHost)

	if err := smtp.SendMail(addr, auth, email, allRecipients, msgBytes); err != nil {
		return nil, fmt.Errorf("comm.outlook_send: %w", err)
	}

	result := workflow.NewItem(map[string]interface{}{
		"sent": true,
		"from": email,
		"to":   toStringInterfaceSlice(toAddrs),
	})
	return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{result}}}, nil
}
