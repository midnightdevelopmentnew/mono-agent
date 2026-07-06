package comm

import (
	"context"
	"strings"
	"testing"

	"monoagent/internal/workflow"
)

// TestNodesAcceptRegistryCredentialKeys is a regression test: each node must
// read the exact config key name that internal/connections/registry.go injects
// for its platform (or, for self-contained schemas, the key its own schema
// JSON declares), not a differently-named key nothing ever populates. Supplying
// config using the real key names should get past field validation and attempt
// a network call, instead of failing immediately with a "required" error.
func TestNodesAcceptRegistryCredentialKeys(t *testing.T) {
	cases := []struct {
		name   string
		node   workflow.NodeExecutor
		config map[string]interface{}
	}{
		{"comm.discord (bot_token)", &DiscordNode{}, map[string]interface{}{
			"bot_token": "tok", "operation": "get_channels", "guild_id": "123",
		}},
		{"comm.slack (access_token)", &SlackNode{}, map[string]interface{}{
			"access_token": "tok", "operation": "list_channels",
		}},
		{"comm.twilio (operation)", &TwilioNode{}, map[string]interface{}{
			"account_sid": "AC123", "auth_token": "tok", "from": "+15551234567", "to": "+15557654321",
			"operation": "send_sms", "body": "hi",
		}},
		{"comm.whatsapp (operation)", &WhatsAppNode{}, map[string]interface{}{
			"access_token": "tok", "phone_number_id": "123", "to": "15551234567",
			"operation": "send_message", "text": "hi",
		}},
	}

	for _, tc := range cases {
		_, err := tc.node.Execute(context.Background(), workflow.NodeInput{}, tc.config)
		if err == nil {
			continue
		}
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "is required") {
			t.Errorf("%s: got a 'required' validation error with the correct key set: %v", tc.name, err)
		}
	}
}
