package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAnthropicComplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify headers
		if got := r.Header.Get("x-api-key"); got != "test-key" {
			t.Errorf("x-api-key = %q, want %q", got, "test-key")
		}
		if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
			t.Errorf("anthropic-version = %q, want %q", got, "2023-06-01")
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want %q", got, "application/json")
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": "Hello from Claude!"},
			},
			"stop_reason": "end_turn",
			"usage": map[string]interface{}{
				"input_tokens":  10,
				"output_tokens": 5,
			},
		})
	}))
	defer srv.Close()

	client := NewAnthropicClient("test-key", srv.URL)
	resp, err := client.Complete(context.Background(), CompletionRequest{
		Model: "claude-sonnet-4-6",
		Messages: []Message{
			{Role: RoleUser, Content: "Hi"},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if resp.Content != "Hello from Claude!" {
		t.Errorf("Content = %q, want %q", resp.Content, "Hello from Claude!")
	}
	if resp.FinishReason != "end_turn" {
		t.Errorf("FinishReason = %q, want %q", resp.FinishReason, "end_turn")
	}
	if resp.Usage.PromptTokens != 10 {
		t.Errorf("PromptTokens = %d, want 10", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != 5 {
		t.Errorf("CompletionTokens = %d, want 5", resp.Usage.CompletionTokens)
	}
	if resp.Usage.TotalTokens != 15 {
		t.Errorf("TotalTokens = %d, want 15", resp.Usage.TotalTokens)
	}
}

func TestAnthropicSystemMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var reqBody map[string]interface{}
		if err := json.Unmarshal(body, &reqBody); err != nil {
			t.Fatalf("unmarshal request: %v", err)
		}

		// Verify system field is present and correct
		system, ok := reqBody["system"]
		if !ok {
			t.Fatal("expected 'system' field in request body")
		}
		if system != "You are a helpful assistant." {
			t.Errorf("system = %q, want %q", system, "You are a helpful assistant.")
		}

		// Verify system message is NOT in the messages array
		messages, ok := reqBody["messages"].([]interface{})
		if !ok {
			t.Fatal("expected 'messages' to be an array")
		}
		for _, m := range messages {
			msg, ok := m.(map[string]interface{})
			if !ok {
				continue
			}
			if msg["role"] == "system" {
				t.Error("system message should not appear in messages array")
			}
		}

		// Verify only user message is in messages
		if len(messages) != 1 {
			t.Errorf("messages len = %d, want 1", len(messages))
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": "I am helpful."},
			},
			"stop_reason": "end_turn",
			"usage": map[string]interface{}{
				"input_tokens":  15,
				"output_tokens": 3,
			},
		})
	}))
	defer srv.Close()

	client := NewAnthropicClient("test-key", srv.URL)
	resp, err := client.Complete(context.Background(), CompletionRequest{
		Model: "claude-sonnet-4-6",
		Messages: []Message{
			{Role: RoleSystem, Content: "You are a helpful assistant."},
			{Role: RoleUser, Content: "Hello"},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if resp.Content != "I am helpful." {
		t.Errorf("Content = %q, want %q", resp.Content, "I am helpful.")
	}
}

func TestAnthropicToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"content": []map[string]interface{}{
				{
					"type":  "tool_use",
					"id":    "tc1",
					"name":  "get_weather",
					"input": map[string]interface{}{"city": "SF"},
				},
			},
			"stop_reason": "tool_use",
			"usage": map[string]interface{}{
				"input_tokens":  20,
				"output_tokens": 10,
			},
		})
	}))
	defer srv.Close()

	client := NewAnthropicClient("test-key", srv.URL)
	resp, err := client.Complete(context.Background(), CompletionRequest{
		Model: "claude-sonnet-4-6",
		Messages: []Message{
			{Role: RoleUser, Content: "What is the weather in SF?"},
		},
		Tools: []ToolDef{
			{
				Type: "function",
				Function: ToolFunction{
					Name:        "get_weather",
					Description: "Get weather for a city",
					Parameters: map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"city": map[string]interface{}{"type": "string"},
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if len(resp.ToolCalls) != 1 {
		t.Fatalf("ToolCalls len = %d, want 1", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "tc1" {
		t.Errorf("ToolCall.ID = %q, want %q", tc.ID, "tc1")
	}
	if tc.Type != "function" {
		t.Errorf("ToolCall.Type = %q, want %q", tc.Type, "function")
	}
	if tc.Function.Name != "get_weather" {
		t.Errorf("ToolCall.Function.Name = %q, want %q", tc.Function.Name, "get_weather")
	}

	// Parse the arguments to verify content (order may vary in JSON)
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
		t.Fatalf("parse ToolCall arguments: %v", err)
	}
	if args["city"] != "SF" {
		t.Errorf("ToolCall args city = %v, want %q", args["city"], "SF")
	}

	if resp.FinishReason != "tool_use" {
		t.Errorf("FinishReason = %q, want %q", resp.FinishReason, "tool_use")
	}
}

// TestAnthropicBaseURLNoDoubleVersion guards the /v1/v1/messages regression:
// the default base URL includes /v1, so the adapter must append the
// unversioned /messages path.
func TestAnthropicBaseURLNoDoubleVersion(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"content":     []map[string]interface{}{{"type": "text", "text": "ok"}},
			"stop_reason": "end_turn",
			"usage":       map[string]interface{}{"input_tokens": 1, "output_tokens": 1},
		})
	}))
	defer srv.Close()

	client := NewAnthropicClient("test-key", srv.URL+"/v1")
	if _, err := client.Complete(context.Background(), CompletionRequest{
		Model:    "claude-sonnet-4-6",
		Messages: []Message{{Role: RoleUser, Content: "Hi"}},
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if gotPath != "/v1/messages" {
		t.Errorf("request path = %q, want %q", gotPath, "/v1/messages")
	}
}

// TestAnthropicToolResultWireFormat verifies that an assistant tool-call turn
// (with empty text) and a tool result are serialized as proper tool_use /
// tool_result content blocks, never as an empty-string message that Anthropic
// would reject.
func TestAnthropicToolResultWireFormat(t *testing.T) {
	var reqBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &reqBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"content":     []map[string]interface{}{{"type": "text", "text": "done"}},
			"stop_reason": "end_turn",
			"usage":       map[string]interface{}{"input_tokens": 1, "output_tokens": 1},
		})
	}))
	defer srv.Close()

	client := NewAnthropicClient("test-key", srv.URL)
	_, err := client.Complete(context.Background(), CompletionRequest{
		Model: "claude-sonnet-4-6",
		Messages: []Message{
			{Role: RoleUser, Content: "make a workflow"},
			{Role: RoleAssistant, Content: "", ToolCalls: []ToolCall{{
				ID: "tu1", Type: "function",
				Function: ToolCallFunc{Name: "create_workflow", Arguments: `{"name":"x"}`},
			}}},
			{Role: RoleTool, Content: `{"workflow_id":"w1"}`, ToolCallID: "tu1"},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	messages, _ := reqBody["messages"].([]interface{})
	if len(messages) != 3 {
		t.Fatalf("messages len = %d, want 3", len(messages))
	}

	asst, _ := messages[1].(map[string]interface{})
	if asst["role"] != "assistant" {
		t.Errorf("messages[1].role = %v, want assistant", asst["role"])
	}
	blocks, ok := asst["content"].([]interface{})
	if !ok {
		t.Fatalf("assistant content is not a block array: %v", asst["content"])
	}
	foundToolUse := false
	for _, b := range blocks {
		bm, _ := b.(map[string]interface{})
		if bm["type"] == "tool_use" {
			foundToolUse = true
			if bm["name"] != "create_workflow" {
				t.Errorf("tool_use name = %v, want create_workflow", bm["name"])
			}
			if bm["id"] != "tu1" {
				t.Errorf("tool_use id = %v, want tu1", bm["id"])
			}
		}
	}
	if !foundToolUse {
		t.Error("assistant message missing tool_use block")
	}

	toolMsg, _ := messages[2].(map[string]interface{})
	if toolMsg["role"] != "user" {
		t.Errorf("tool-result message role = %v, want user", toolMsg["role"])
	}
	trBlocks, ok := toolMsg["content"].([]interface{})
	if !ok || len(trBlocks) == 0 {
		t.Fatalf("tool-result content is not a block array: %v", toolMsg["content"])
	}
	tr, _ := trBlocks[0].(map[string]interface{})
	if tr["type"] != "tool_result" {
		t.Errorf("block type = %v, want tool_result", tr["type"])
	}
	if tr["tool_use_id"] != "tu1" {
		t.Errorf("tool_use_id = %v, want tu1", tr["tool_use_id"])
	}
}

// TestAnthropicStreamToolCallOrder verifies streamed tool calls are emitted in
// content-block index order rather than nondeterministic map order.
func TestAnthropicStreamToolCallOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		events := []string{
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"a\",\"name\":\"first\"}}\n",
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"b\",\"name\":\"second\"}}\n",
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"}}\n",
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n",
		}
		for _, ev := range events {
			fmt.Fprint(w, ev+"\n")
			flusher.Flush()
		}
	}))
	defer srv.Close()

	client := NewAnthropicClient("test-key", srv.URL)
	var got []ToolCall
	err := client.StreamComplete(context.Background(), CompletionRequest{
		Model:    "claude-sonnet-4-6",
		Messages: []Message{{Role: RoleUser, Content: "go"}},
	}, func(chunk StreamChunk) {
		if len(chunk.ToolCalls) > 0 {
			got = chunk.ToolCalls
		}
	})
	if err != nil {
		t.Fatalf("StreamComplete: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("tool calls = %d, want 2", len(got))
	}
	if got[0].Function.Name != "first" || got[1].Function.Name != "second" {
		t.Errorf("tool order = [%s, %s], want [first, second]", got[0].Function.Name, got[1].Function.Name)
	}
}

func TestAnthropicStreamComplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected http.Flusher")
		}

		events := []string{
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hel\"}}\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"lo!\"}}\n",
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n",
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n",
		}
		for _, ev := range events {
			fmt.Fprint(w, ev+"\n")
			flusher.Flush()
		}
	}))
	defer srv.Close()

	client := NewAnthropicClient("test-key", srv.URL)

	var accumulated string
	var gotDone bool

	err := client.StreamComplete(context.Background(), CompletionRequest{
		Model: "claude-sonnet-4-6",
		Messages: []Message{
			{Role: RoleUser, Content: "Hi"},
		},
	}, func(chunk StreamChunk) {
		accumulated += chunk.Content
		if chunk.Done {
			gotDone = true
		}
	})
	if err != nil {
		t.Fatalf("StreamComplete: %v", err)
	}

	if accumulated != "Hello!" {
		t.Errorf("accumulated = %q, want %q", accumulated, "Hello!")
	}
	if !gotDone {
		t.Error("expected Done=true chunk")
	}
}
