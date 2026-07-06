package workflow

import (
	"testing"
)

func TestLoadDefaultSchema_KnownType(t *testing.T) {
	schema, err := LoadDefaultSchema("service.google_sheets")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(schema.Fields) == 0 {
		t.Fatal("expected fields for service.google_sheets")
	}
	var found bool
	for _, f := range schema.Fields {
		if f.Key == "spreadsheet_id" && f.Type == "resource_picker" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected spreadsheet_id resource_picker field")
	}
}

func TestLoadDefaultSchema_UnknownType(t *testing.T) {
	schema, err := LoadDefaultSchema("unknown.node_type")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if schema == nil {
		t.Fatal("expected non-nil schema for unknown type")
	}
	if len(schema.Fields) != 0 {
		t.Fatalf("expected empty fields for unknown type, got %d", len(schema.Fields))
	}
}

func TestLoadDefaultSchema_BrowserFallback(t *testing.T) {
	cases := []struct {
		nodeType    string
		expectField string
	}{
		{"linkedin.find_by_keyword", "keywords"},
		{"instagram.send_dms", "targets"},
		{"x.engage_with_posts", "keywords"},
		{"tiktok.export_followers", "targets"},
		{"instagram.publish_post", "text"},
		{"linkedin.scrape_profile_info", "targets"},
	}
	for _, tc := range cases {
		schema, err := LoadDefaultSchema(tc.nodeType)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", tc.nodeType, err)
		}
		if len(schema.Fields) == 0 {
			t.Fatalf("%s: expected fields, got none", tc.nodeType)
		}
		var found bool
		for _, f := range schema.Fields {
			if f.Key == tc.expectField {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: expected field %q", tc.nodeType, tc.expectField)
		}
	}
}

func TestListEmbeddedSchemas(t *testing.T) {
	types := ListEmbeddedSchemas()
	if len(types) < 50 {
		t.Fatalf("expected at least 50 embedded schemas, got %d", len(types))
	}
}

// TestLoadDefaultSchema_OutlookAndHumanInLoop is a regression test: these node
// types previously had no schema file, so LoadDefaultSchema silently fell back
// to an empty-fields schema and the canvas config panel rendered no inputs.
func TestLoadDefaultSchema_OutlookAndHumanInLoop(t *testing.T) {
	cases := []struct {
		nodeType    string
		expectField string
	}{
		{"comm.outlook_send", "email"},
		{"comm.outlook_read", "email"},
		{"core.human_in_loop", "editable_fields"},
	}
	for _, tc := range cases {
		schema, err := LoadDefaultSchema(tc.nodeType)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", tc.nodeType, err)
		}
		if len(schema.Fields) == 0 {
			t.Fatalf("%s: expected non-empty fields, got none", tc.nodeType)
		}
		var found bool
		for _, f := range schema.Fields {
			if f.Key == tc.expectField {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: expected field %q", tc.nodeType, tc.expectField)
		}
	}
}

// TestLoadDefaultSchema_AINodes is a regression test: the six ai.* node types
// (ai.chat, ai.extract, ai.classify, ai.transform, ai.embed, ai.agent) had no
// schema file at all, so the main workflow canvas rendered them with zero
// config fields — every one of them always failed with "provider_id is
// required" since there was no way to set it through that UI.
func TestLoadDefaultSchema_AINodes(t *testing.T) {
	types := []string{"ai.chat", "ai.extract", "ai.classify", "ai.transform", "ai.embed", "ai.agent"}
	for _, nodeType := range types {
		schema, err := LoadDefaultSchema(nodeType)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", nodeType, err)
		}
		if len(schema.Fields) == 0 {
			t.Fatalf("%s: expected non-empty fields, got none", nodeType)
		}
		var hasProviderID bool
		for _, f := range schema.Fields {
			if f.Key == "provider_id" {
				hasProviderID = true
			}
		}
		if !hasProviderID {
			t.Errorf("%s: expected a provider_id field", nodeType)
		}
	}
}
