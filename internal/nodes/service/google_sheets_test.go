package service

import (
	"encoding/base64"
	"strings"
	"testing"

	"monoagent/internal/workflow"
)

func TestSheetsItemsToRows_HeaderOptIn(t *testing.T) {
	items := []workflow.Item{
		workflow.NewItem(map[string]interface{}{"a": "1", "b": "2"}),
	}

	// Without header (default for append): only the data row, no header row.
	rows := sheetsItemsToRows(items, false)
	if len(rows) != 1 {
		t.Fatalf("withHeader=false: expected 1 row, got %d", len(rows))
	}
	if rows[0][0] != "1" {
		t.Errorf("withHeader=false: first row should be data, got %v", rows[0])
	}

	// With header opt-in: header row prepended.
	rows = sheetsItemsToRows(items, true)
	if len(rows) != 2 {
		t.Fatalf("withHeader=true: expected 2 rows, got %d", len(rows))
	}
	if rows[0][0] != "a" || rows[0][1] != "b" {
		t.Errorf("withHeader=true: first row should be header, got %v", rows[0])
	}
}

func TestGmailSanitizeHeader_StripsCRLF(t *testing.T) {
	got := gmailSanitizeHeader("Invoice\r\nBcc: attacker@evil.com")
	if strings.ContainsAny(got, "\r\n") {
		t.Fatalf("sanitized header still contains CR/LF: %q", got)
	}

	raw, err := gmailBuildRFC2822(gmailMessageOpts{
		To:       "victim@example.com",
		Subject:  "Invoice\r\nBcc: attacker@evil.com",
		Body:     "hi",
		BodyType: "text",
	})
	if err != nil {
		t.Fatalf("build error: %v", err)
	}
	decoded, err := base64.URLEncoding.DecodeString(raw)
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}
	for _, line := range strings.Split(string(decoded), "\r\n") {
		if strings.HasPrefix(line, "Bcc:") {
			t.Errorf("injected Bcc header survived as its own line:\n%s", decoded)
		}
	}
}

func TestShopifyValidShop(t *testing.T) {
	valid := []string{"mystore", "my-store", "Store123"}
	for _, s := range valid {
		if !shopifyValidShop(s) {
			t.Errorf("shopifyValidShop(%q) = false, want true", s)
		}
	}
	invalid := []string{"", "evil.com/x?", "evil.com", "store/path", "store?q=1", "a@b", "sub.domain"}
	for _, s := range invalid {
		if shopifyValidShop(s) {
			t.Errorf("shopifyValidShop(%q) = true, want false", s)
		}
	}
}

func TestSheetsValuesToItems_RowIndex(t *testing.T) {
	values := []interface{}{
		[]interface{}{"title", "status"},
		[]interface{}{"Post 1", "todo"},
		[]interface{}{"Post 2", "done"},
	}

	items := sheetsValuesToItems(values, true)

	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}

	if got := items[0].JSON["_row_index"]; got != 2 {
		t.Errorf("items[0]._row_index = %v, want 2", got)
	}
	if got := items[1].JSON["_row_index"]; got != 3 {
		t.Errorf("items[1]._row_index = %v, want 3", got)
	}

	// Without header row: first data row = sheet row 1
	itemsNoHeader := sheetsValuesToItems(values, false)
	if got := itemsNoHeader[0].JSON["_row_index"]; got != 1 {
		t.Errorf("no-header items[0]._row_index = %v, want 1", got)
	}
}
