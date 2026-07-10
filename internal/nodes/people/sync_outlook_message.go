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
	// "inbound" (default) syncs the sender of each message as the person —
	// use for an inbox/received-mail read. "outbound" syncs each recipient
	// as the person instead — use for a sentitems/sent-mail read, where the
	// sender is always the account's own address.
	direction, _ := config["direction"].(string)
	if direction == "" {
		direction = "inbound"
	}

	var savedItems []workflow.Item

	for _, item := range input.Items {
		data := item.JSON

		subject, _ := data["subject"].(string)
		body, _ := data["bodyPreview"].(string)
		if bodyObj, ok := data["body"].(map[string]interface{}); ok {
			if full, ok := bodyObj["content"].(string); ok && full != "" {
				body = full
			}
		}
		messageID, _ := data["id"].(string)
		var sentAt time.Time
		if receivedRaw, _ := data["receivedDateTime"].(string); receivedRaw != "" {
			if t, err := time.Parse(time.RFC3339, receivedRaw); err == nil {
				sentAt = t
			}
		}

		var counterparts []map[string]interface{}
		var fromAddr string
		if direction == "outbound" {
			fromObj, _ := data["from"].(map[string]interface{})
			fromEmailAddr, _ := fromObj["emailAddress"].(map[string]interface{})
			fromAddr, _ = fromEmailAddr["address"].(string)
			toRecipients, _ := data["toRecipients"].([]interface{})
			for _, r := range toRecipients {
				rm, ok := r.(map[string]interface{})
				if !ok {
					continue
				}
				if ea, ok := rm["emailAddress"].(map[string]interface{}); ok {
					counterparts = append(counterparts, ea)
				}
			}
		} else {
			fromObj, _ := data["from"].(map[string]interface{})
			if ea, ok := fromObj["emailAddress"].(map[string]interface{}); ok {
				counterparts = append(counterparts, ea)
			}
		}

		for _, ea := range counterparts {
			counterpartEmail, _ := ea["address"].(string)
			counterpartName, _ := ea["name"].(string)
			if counterpartEmail == "" {
				continue // Cannot upsert a person without an identifying email.
			}

			person := &storage.Person{
				PlatformUsername: counterpartEmail,
				Platform:         "email",
				FullName:         counterpartName,
				ProfileID:        profileID,
			}
			if err := db.UpsertPerson(person); err != nil {
				return nil, fmt.Errorf("people.sync_outlook_message: upsert person %s: %w", counterpartEmail, err)
			}
			// UpsertPerson only fills in person.ID when the caller pre-set it;
			// re-fetch to get the resolved ID for the (possibly pre-existing) row.
			resolved, err := db.GetPersonByUsername(counterpartEmail, "email", profileID)
			if err != nil {
				return nil, fmt.Errorf("people.sync_outlook_message: resolve person %s: %w", counterpartEmail, err)
			}
			if resolved == nil {
				return nil, fmt.Errorf("people.sync_outlook_message: person %s not found after upsert", counterpartEmail)
			}

			sender := counterpartEmail
			if direction == "outbound" {
				sender = fromAddr
			}
			msg := &storage.PersonMessage{
				PersonID:   resolved.ID,
				Source:     source,
				ExternalID: messageID,
				Direction:  direction,
				Sender:     sender,
				Subject:    subject,
				Body:       body,
				SentAt:     sentAt,
			}
			if err := db.UpsertPersonMessage(msg, profileID); err != nil {
				return nil, fmt.Errorf("people.sync_outlook_message: upsert message for %s: %w", counterpartEmail, err)
			}

			out := map[string]interface{}{
				"person_id":  resolved.ID,
				"email":      counterpartEmail,
				"message_id": messageID,
			}
			savedItems = append(savedItems, workflow.NewItem(out))
		}
	}

	return []workflow.NodeOutput{{Handle: "main", Items: savedItems}}, nil
}
