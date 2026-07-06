package comm

import "testing"

// TestResolveTelegramChatID is a regression test: the schema's chat_id help
// text documents "@channel_username" as a valid value, but the code
// previously always tried to parse chat_id as a numeric string and rejected
// any "@username" value.
func TestResolveTelegramChatID(t *testing.T) {
	cases := []struct {
		name         string
		input        interface{}
		wantChatID   int64
		wantUsername string
		wantErr      bool
	}{
		{"numeric int64", int64(123), 123, "", false},
		{"numeric float64 (JSON)", float64(456), 456, "", false},
		{"numeric string", "789", 789, "", false},
		{"channel username", "@mychannel", 0, "@mychannel", false},
		{"invalid string", "not-a-number", 0, "", true},
		{"nil", nil, 0, "", true},
	}
	for _, tc := range cases {
		chatID, username, err := resolveTelegramChatID(tc.input)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%s: expected error, got nil", tc.name)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
			continue
		}
		if chatID != tc.wantChatID || username != tc.wantUsername {
			t.Errorf("%s: got (%d, %q), want (%d, %q)", tc.name, chatID, username, tc.wantChatID, tc.wantUsername)
		}
	}
}
