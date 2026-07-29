package service

import (
	"context"
	"testing"
)

func TestDownloadAttachmentsEnabled_DefaultsOn(t *testing.T) {
	if !downloadAttachmentsEnabled(map[string]interface{}{}) {
		t.Error("attachments should download by default")
	}
	if downloadAttachmentsEnabled(map[string]interface{}{"download_attachments": false}) {
		t.Error("download_attachments:false must disable downloading")
	}
}

func TestEnrichOutlookMessage_StampsProvenance(t *testing.T) {
	msg := map[string]interface{}{
		"id":             "AAMk123=",
		"webLink":        "https://outlook.office365.com/mail/id/AAMk123",
		"hasAttachments": false,
	}
	// download=false keeps this offline; a message with no attachments would
	// not issue a request either way.
	enrichOutlookMessage(context.Background(), msg, "token", "sentitems", false)

	src, ok := msg["_source"].(map[string]interface{})
	if !ok {
		t.Fatalf("no _source stamped: %v", msg)
	}
	if got, _ := src["source"].(string); got != "outlook" {
		t.Errorf("source = %q, want outlook", got)
	}
	if got, _ := src["folder"].(string); got != "sentitems" {
		t.Errorf("folder = %q, want the mailbox that was read", got)
	}
	if got, _ := src["external_id"].(string); got != "AAMk123=" {
		t.Errorf("external_id = %q, want the Graph message id", got)
	}
	if got, _ := src["web_link"].(string); got == "" {
		t.Error("web_link missing — a reader cannot go look at the original")
	}
	if got, _ := src["fetched_at"].(string); got == "" {
		t.Error("fetched_at missing")
	}
}

func TestEnrichOutlookMessage_DefaultsFolder(t *testing.T) {
	msg := map[string]interface{}{"id": "x"}
	enrichOutlookMessage(context.Background(), msg, "token", "", false)

	src, _ := msg["_source"].(map[string]interface{})
	if got, _ := src["folder"].(string); got != "inbox" {
		t.Errorf("folder = %q, want inbox when unspecified", got)
	}
}
