package chat

import (
	"context"
	"database/sql"
	"sync"
	"testing"

	"github.com/zalando/go-keyring"

	"monoagent/internal/ai"
	"monoagent/internal/storage"
)

// --- mock AI client ---

type mockAIClient struct {
	response string
}

func (m *mockAIClient) Complete(ctx context.Context, req ai.CompletionRequest) (ai.CompletionResponse, error) {
	return ai.CompletionResponse{Content: m.response, FinishReason: "stop"}, nil
}

func (m *mockAIClient) StreamComplete(ctx context.Context, req ai.CompletionRequest, onChunk func(ai.StreamChunk)) error {
	onChunk(ai.StreamChunk{Content: m.response, Done: true})
	return nil
}

// --- helpers ---

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	keyring.MockInit()
	db, err := storage.NewDatabase(t.TempDir() + "/chat-test.db")
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	t.Cleanup(func() { db.DB.Close() })
	return db.DB
}

// seedWorkflow registers a workflow row owned by the default profile, so
// checkWorkflowOwnership (called by StreamChat/GetHistory/ClearHistory) allows it.
func seedWorkflow(t *testing.T, db *sql.DB, workflowID string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO workflows (id, name, profile_id) VALUES (?, 'Test Workflow', 'default')`, workflowID); err != nil {
		t.Fatalf("seed workflow %s: %v", workflowID, err)
	}
}

func newTestService(t *testing.T, mockResp string, workflowIDs ...string) *ChatService {
	t.Helper()
	db := openTestDB(t)
	for _, id := range workflowIDs {
		seedWorkflow(t, db, id)
	}
	store, err := ai.NewAIStore(db)
	if err != nil {
		t.Fatalf("NewAIStore: %v", err)
	}

	// Seed a provider so GetProvider succeeds.
	if err := store.SaveProvider(ai.AIProvider{
		ID:         "test-provider",
		Name:       "Test",
		ProviderID: "openai",
		Tier:       "known",
		APIKey:     "sk-test",
		Status:     "active",
	}); err != nil {
		t.Fatalf("SaveProvider: %v", err)
	}

	svc := NewChatService(store, db)
	// Override the client factory so we don't need real provider wiring.
	mock := &mockAIClient{response: mockResp}
	svc.newClientFn = func(provider ai.AIProvider) (ai.AIClient, error) {
		return mock, nil
	}
	return svc
}

// --- tests ---

func TestStreamChatBasic(t *testing.T) {
	svc := newTestService(t, "Hello from AI!", "wf-1")

	var mu sync.Mutex
	var chunks []ai.StreamChunk

	err := svc.StreamChat(
		context.Background(),
		"wf-1",
		"Hi there",
		"test-provider",
		"gpt-4o",
		func(chunk ai.StreamChunk) {
			mu.Lock()
			chunks = append(chunks, chunk)
			mu.Unlock()
		},
		nil, // no tool calls expected
	)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}

	// Verify onChunk was called at least once with the AI response.
	mu.Lock()
	defer mu.Unlock()
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk, got none")
	}
	found := false
	for _, c := range chunks {
		if c.Content == "Hello from AI!" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected chunk with content %q, got %v", "Hello from AI!", chunks)
	}

	// Verify messages were persisted: user + assistant.
	history, err := svc.GetHistory("wf-1")
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 messages in history, got %d", len(history))
	}
	if history[0].Role != ai.RoleUser {
		t.Errorf("first message role = %q, want %q", history[0].Role, ai.RoleUser)
	}
	if history[0].Content != "Hi there" {
		t.Errorf("first message content = %q, want %q", history[0].Content, "Hi there")
	}
	if history[1].Role != ai.RoleAssistant {
		t.Errorf("second message role = %q, want %q", history[1].Role, ai.RoleAssistant)
	}
	if history[1].Content != "Hello from AI!" {
		t.Errorf("second message content = %q, want %q", history[1].Content, "Hello from AI!")
	}
}

func TestGetHistory(t *testing.T) {
	svc := newTestService(t, "Response 1", "wf-2")

	// Send two messages to build history.
	err := svc.StreamChat(context.Background(), "wf-2", "First message", "test-provider", "gpt-4o", nil, nil)
	if err != nil {
		t.Fatalf("StreamChat 1: %v", err)
	}
	svc.newClientFn = func(provider ai.AIProvider) (ai.AIClient, error) {
		return &mockAIClient{response: "Response 2"}, nil
	}
	err = svc.StreamChat(context.Background(), "wf-2", "Second message", "test-provider", "gpt-4o", nil, nil)
	if err != nil {
		t.Fatalf("StreamChat 2: %v", err)
	}

	history, err := svc.GetHistory("wf-2")
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	// 2 user messages + 2 assistant messages = 4
	if len(history) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(history))
	}
	if history[0].Content != "First message" {
		t.Errorf("history[0].Content = %q, want %q", history[0].Content, "First message")
	}
	if history[1].Content != "Response 1" {
		t.Errorf("history[1].Content = %q, want %q", history[1].Content, "Response 1")
	}
	if history[2].Content != "Second message" {
		t.Errorf("history[2].Content = %q, want %q", history[2].Content, "Second message")
	}
	if history[3].Content != "Response 2" {
		t.Errorf("history[3].Content = %q, want %q", history[3].Content, "Response 2")
	}
}

func TestClearHistory(t *testing.T) {
	svc := newTestService(t, "Some response", "wf-3")

	// Create some history.
	err := svc.StreamChat(context.Background(), "wf-3", "Hello", "test-provider", "gpt-4o", nil, nil)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}

	history, err := svc.GetHistory("wf-3")
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history) == 0 {
		t.Fatal("expected non-empty history before clear")
	}

	// Clear.
	if err := svc.ClearHistory("wf-3"); err != nil {
		t.Fatalf("ClearHistory: %v", err)
	}

	history, err = svc.GetHistory("wf-3")
	if err != nil {
		t.Fatalf("GetHistory after clear: %v", err)
	}
	if len(history) != 0 {
		t.Errorf("expected empty history after clear, got %d messages", len(history))
	}
}
