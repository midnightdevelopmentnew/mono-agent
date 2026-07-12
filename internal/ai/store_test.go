package ai

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// TestProviderJSONOmitsAPIKey is a regression test: serializing an AIProvider
// (as ListAIProviders/SaveAIProvider do for the GUI) must never leak the
// plaintext API key.
func TestProviderJSONOmitsAPIKey(t *testing.T) {
	p := AIProvider{
		ID:         "p1",
		Name:       "My OpenAI",
		ProviderID: "openai",
		Tier:       "known",
		APIKey:     "sk-supersecret",
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "sk-supersecret") {
		t.Errorf("serialized provider leaked API key: %s", b)
	}
	// A slice (as returned by ListProviders) must also be redacted.
	b2, _ := json.Marshal([]AIProvider{p})
	if strings.Contains(string(b2), "sk-supersecret") {
		t.Errorf("serialized provider slice leaked API key: %s", b2)
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestStoreInitTables(t *testing.T) {
	db := openTestDB(t)
	store, err := NewAIStore(db)
	if err != nil {
		t.Fatalf("NewAIStore: %v", err)
	}
	_ = store

	// Verify ai_providers table exists by inserting a raw row.
	_, err = db.Exec(`INSERT INTO ai_providers (id, name, provider_id, tier, api_key, created_at)
		VALUES ('test', 'Test', 'openai', 'known', 'sk-xxx', '2025-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert into ai_providers: %v", err)
	}

	// Verify ai_chat_messages table exists by inserting a raw row.
	_, err = db.Exec(`INSERT INTO ai_chat_messages (id, workflow_id, role, content, created_at)
		VALUES ('msg1', 'wf1', 'user', 'hello', '2025-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert into ai_chat_messages: %v", err)
	}
}

func TestProviderCRUD(t *testing.T) {
	db := openTestDB(t)
	store, err := NewAIStore(db)
	if err != nil {
		t.Fatalf("NewAIStore: %v", err)
	}

	p := AIProvider{
		ID:           "p1",
		Name:         "My OpenAI",
		ProviderID:   "openai",
		Tier:         "known",
		APIKey:       "sk-abc",
		BaseURL:      "https://api.openai.com/v1",
		DefaultModel: "gpt-4o",
		Status:       "untested",
	}

	// Save
	if err := store.SaveProvider(p); err != nil {
		t.Fatalf("SaveProvider: %v", err)
	}

	// Get
	got, err := store.GetProvider("p1", "default")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if got.Name != "My OpenAI" {
		t.Errorf("Name = %q, want %q", got.Name, "My OpenAI")
	}
	if got.APIKey != "sk-abc" {
		t.Errorf("APIKey = %q, want %q", got.APIKey, "sk-abc")
	}
	if got.CreatedAt == "" {
		t.Error("CreatedAt should be auto-set when empty")
	}

	// List
	providers, err := store.ListProviders("default")
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(providers) != 1 {
		t.Fatalf("ListProviders len = %d, want 1", len(providers))
	}

	// Update (re-save with new name)
	p.Name = "My OpenAI Updated"
	p.CreatedAt = got.CreatedAt // preserve original timestamp
	if err := store.SaveProvider(p); err != nil {
		t.Fatalf("SaveProvider (update): %v", err)
	}
	got2, err := store.GetProvider("p1", "default")
	if err != nil {
		t.Fatalf("GetProvider after update: %v", err)
	}
	if got2.Name != "My OpenAI Updated" {
		t.Errorf("Name after update = %q, want %q", got2.Name, "My OpenAI Updated")
	}

	// Delete
	if err := store.DeleteProvider("p1", "default"); err != nil {
		t.Fatalf("DeleteProvider: %v", err)
	}
	_, err = store.GetProvider("p1", "default")
	if err != sql.ErrNoRows {
		t.Errorf("GetProvider after delete: err = %v, want sql.ErrNoRows", err)
	}
}

// TestProviderProfileIsolation is a regression test: a provider saved under one
// profile must not be visible, testable, or deletable from a different profile.
func TestProviderProfileIsolation(t *testing.T) {
	db := openTestDB(t)
	store, err := NewAIStore(db)
	if err != nil {
		t.Fatalf("NewAIStore: %v", err)
	}

	p := AIProvider{ID: "p-a", Name: "Profile A Key", ProviderID: "openai", Tier: "known", APIKey: "sk-a", ProfileID: "profile-a"}
	if err := store.SaveProvider(p); err != nil {
		t.Fatalf("SaveProvider: %v", err)
	}

	if _, err := store.GetProvider("p-a", "profile-b"); err == nil {
		t.Error("GetProvider: expected error reading another profile's provider, got nil")
	}
	list, err := store.ListProviders("profile-b")
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("ListProviders(profile-b) = %d providers, want 0", len(list))
	}
	if err := store.DeleteProvider("p-a", "profile-b"); err != nil {
		t.Fatalf("DeleteProvider from wrong profile should not error (no rows affected): %v", err)
	}
	if _, err := store.GetProvider("p-a", "profile-a"); err != nil {
		t.Errorf("provider should still exist after cross-profile delete attempt: %v", err)
	}
}

func TestChatMessageCRUD(t *testing.T) {
	db := openTestDB(t)
	store, err := NewAIStore(db)
	if err != nil {
		t.Fatalf("NewAIStore: %v", err)
	}

	wfID := "workflow-1"

	// Save messages
	m1 := ChatMessage{
		ID:         "m1",
		WorkflowID: wfID,
		Role:       "user",
		Content:    "Hello",
		CreatedAt:  "2025-01-01T00:00:01Z",
	}
	m2 := ChatMessage{
		ID:         "m2",
		WorkflowID: wfID,
		Role:       "assistant",
		Content:    "Hi there!",
		ProviderID: "p1",
		Model:      "gpt-4o",
		TokenCount: 42,
		CreatedAt:  "2025-01-01T00:00:02Z",
	}

	if err := store.SaveChatMessage(m1); err != nil {
		t.Fatalf("SaveChatMessage m1: %v", err)
	}
	if err := store.SaveChatMessage(m2); err != nil {
		t.Fatalf("SaveChatMessage m2: %v", err)
	}

	// Get history
	history, err := store.GetChatHistory(wfID)
	if err != nil {
		t.Fatalf("GetChatHistory: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("GetChatHistory len = %d, want 2", len(history))
	}
	if history[0].ID != "m1" || history[1].ID != "m2" {
		t.Errorf("history order: got [%s, %s], want [m1, m2]", history[0].ID, history[1].ID)
	}
	if history[1].TokenCount != 42 {
		t.Errorf("TokenCount = %d, want 42", history[1].TokenCount)
	}

	// Clear history
	if err := store.ClearChatHistory(wfID); err != nil {
		t.Fatalf("ClearChatHistory: %v", err)
	}
	history, err = store.GetChatHistory(wfID)
	if err != nil {
		t.Fatalf("GetChatHistory after clear: %v", err)
	}
	if len(history) != 0 {
		t.Errorf("GetChatHistory after clear: len = %d, want 0", len(history))
	}
}

func TestProviderStatus(t *testing.T) {
	db := openTestDB(t)
	store, err := NewAIStore(db)
	if err != nil {
		t.Fatalf("NewAIStore: %v", err)
	}

	p := AIProvider{
		ID:         "ps1",
		Name:       "Status Test",
		ProviderID: "openai",
		Tier:       "known",
		APIKey:     "sk-test",
		Status:     "untested",
	}
	if err := store.SaveProvider(p); err != nil {
		t.Fatalf("SaveProvider: %v", err)
	}

	// Verify initial status
	got, err := store.GetProvider("ps1", "default")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if got.Status != "untested" {
		t.Errorf("initial Status = %q, want %q", got.Status, "untested")
	}

	// Update status
	testedAt := "2025-06-15T12:00:00Z"
	if err := store.UpdateProviderStatus("ps1", "active", testedAt, "default"); err != nil {
		t.Fatalf("UpdateProviderStatus: %v", err)
	}

	got, err = store.GetProvider("ps1", "default")
	if err != nil {
		t.Fatalf("GetProvider after status update: %v", err)
	}
	if got.Status != "active" {
		t.Errorf("Status = %q, want %q", got.Status, "active")
	}
	if got.LastTested != testedAt {
		t.Errorf("LastTested = %q, want %q", got.LastTested, testedAt)
	}
}
