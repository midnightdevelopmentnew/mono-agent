package peoplenodes

import (
	"context"
	"fmt"
	"time"

	"monoagent/internal/storage"
	"monoagent/internal/workflow"
)

// SyncOutlookMessageNode upserts a Person (keyed by sender email) and a
// PersonMessage for each Microsoft Graph message item produced by
// service.outlook_mail's list_messages operation. Rows that don't already
// exist in People are created; existing ones are matched by email address.
// Type: "people.sync_outlook_message"
type SyncOutlookMessageNode struct{}

func (n *SyncOutlookMessageNode) Type() string { return "people.sync_outlook_message" }

func (n *SyncOutlookMessageNode) Execute(
	ctx context.Context,
	input workflow.NodeInput,
	config map[string]interface{},
) ([]workflow.NodeOutput, error) {
	if globalPeopleDB == nil {
		return nil, fmt.Errorf("people.sync_outlook_message: database not available")
	}
	db := &storage.Database{DB: globalPeopleDB}

	profileID, _ := config["profile_id"].(string)
	if profileID == "" {
		profileID = "default"
	}
	source, _ := config["source"].(string)
	if source == "" {
		source = "outlook"
	}

	var savedItems []workflow.Item

	for _, item := range input.Items {
		data := item.JSON

		fromObj, _ := data["from"].(map[string]interface{})
		emailAddr, _ := fromObj["emailAddress"].(map[string]interface{})
		senderEmail, _ := emailAddr["address"].(string)
		senderName, _ := emailAddr["name"].(string)
		if senderEmail == "" {
			continue // Cannot upsert a person without an identifying email.
		}

		person := &storage.Person{
			PlatformUsername: senderEmail,
			Platform:         "email",
			FullName:         senderName,
			ProfileID:        profileID,
		}
		if err := db.UpsertPerson(person); err != nil {
			return nil, fmt.Errorf("people.sync_outlook_message: upsert person %s: %w", senderEmail, err)
		}
		// UpsertPerson only fills in person.ID when the caller pre-set it;
		// re-fetch to get the resolved ID for the (possibly pre-existing) row.
		resolved, err := db.GetPersonByUsername(senderEmail, "email", profileID)
		if err != nil {
			return nil, fmt.Errorf("people.sync_outlook_message: resolve person %s: %w", senderEmail, err)
		}
		if resolved == nil {
			return nil, fmt.Errorf("people.sync_outlook_message: person %s not found after upsert", senderEmail)
		}

		subject, _ := data["subject"].(string)
		body, _ := data["bodyPreview"].(string)
		if bodyObj, ok := data["body"].(map[string]interface{}); ok {
			if full, ok := bodyObj["content"].(string); ok && full != "" {
				body = full
			}
		}
		messageID, _ := data["id"].(string)

		msg := &storage.PersonMessage{
			PersonID:   resolved.ID,
			Source:     source,
			ExternalID: messageID,
			Direction:  "inbound",
			Sender:     senderEmail,
			Subject:    subject,
			Body:       body,
		}
		if receivedRaw, _ := data["receivedDateTime"].(string); receivedRaw != "" {
			if t, err := time.Parse(time.RFC3339, receivedRaw); err == nil {
				msg.SentAt = t
			}
		}
		if err := db.UpsertPersonMessage(msg, profileID); err != nil {
			return nil, fmt.Errorf("people.sync_outlook_message: upsert message from %s: %w", senderEmail, err)
		}

		out := map[string]interface{}{
			"person_id":    resolved.ID,
			"sender_email": senderEmail,
			"message_id":   messageID,
		}
		savedItems = append(savedItems, workflow.NewItem(out))
	}

	return []workflow.NodeOutput{{Handle: "main", Items: savedItems}}, nil
}
