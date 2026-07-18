package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"monoagent/internal/ai"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// ─────────────────────────────────────────────────────────────────────────────
// AI Providers
// ─────────────────────────────────────────────────────────────────────────────

func (a *App) ListAIProviders() string {
	if a.aiStore == nil {
		return "[]"
	}
	providers, err := a.aiStore.ListProviders(a.getActiveProfileID())
	if err != nil {
		return aiError(err)
	}
	b, _ := json.Marshal(providers)
	return string(b)
}

func (a *App) SaveAIProvider(providerJSON string) string {
	if a.aiStore == nil {
		return aiError(fmt.Errorf("ai store not initialized"))
	}
	var p ai.AIProvider
	if err := json.Unmarshal([]byte(providerJSON), &p); err != nil {
		return aiError(err)
	}
	if p.ID == "" {
		p.ID = newUUID()
	} else if _, err := a.aiStore.GetProvider(p.ID, a.getActiveProfileID()); err != nil {
		return aiError(fmt.Errorf("provider %s not found", p.ID))
	}
	p.ProfileID = a.getActiveProfileID()
	if err := a.aiStore.SaveProvider(p); err != nil {
		return aiError(err)
	}
	b, _ := json.Marshal(p)
	return string(b)
}

func (a *App) DeleteAIProvider(id string) string {
	if a.aiStore == nil {
		return aiError(fmt.Errorf("ai store not initialized"))
	}
	if err := a.aiStore.DeleteProvider(id, a.getActiveProfileID()); err != nil {
		return aiError(err)
	}
	return `{"ok":true}`
}

func (a *App) TestAIProvider(id string) string {
	if a.aiStore == nil {
		return aiError(fmt.Errorf("ai store not initialized"))
	}
	p, err := a.aiStore.GetProvider(id, a.getActiveProfileID())
	if err != nil {
		return aiError(err)
	}
	client, err := ai.NewClient(p)
	if err != nil {
		return aiError(err)
	}
	model := p.DefaultModel
	if model == "" {
		def, ok := ai.GetProviderDef(p.ProviderID)
		if ok && len(def.Models) > 0 {
			model = def.Models[0].ID
		} else {
			model = "gpt-4o-mini"
		}
	}
	_, err = client.Complete(context.Background(), ai.CompletionRequest{
		Model:     model,
		Messages:  []ai.Message{{Role: ai.RoleUser, Content: "Say ok"}},
		MaxTokens: 5,
	})
	status := "active"
	if err != nil {
		status = "error"
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_ = a.aiStore.UpdateProviderStatus(id, status, now, a.getActiveProfileID())
	if err != nil {
		return fmt.Sprintf(`{"status":"error","error":%q}`, err.Error())
	}
	return `{"status":"active"}`
}

func (a *App) GetAIModels(providerID string) string {
	def, ok := ai.GetProviderDef(providerID)
	if !ok {
		return "[]"
	}
	b, _ := json.Marshal(def.Models)
	return string(b)
}

func (a *App) GetAIRegistry() string {
	b, _ := json.Marshal(ai.ProviderRegistry)
	return string(b)
}

func aiError(err error) string {
	return fmt.Sprintf(`{"error":%q}`, err.Error())
}

// ─────────────────────────────────────────────────────────────────────────────
// AI Chat
// ─────────────────────────────────────────────────────────────────────────────

func (a *App) StreamAIChat(workflowID, message, providerID, model string) string {
	if a.chatService == nil {
		return aiError(fmt.Errorf("chat service not initialized"))
	}
	a.chatService.SetProfileID(a.getActiveProfileID())
	// Cancellable, parented to the app context so shutdown stops the stream.
	ctx, cancel := context.WithCancel(a.ctx)
	h := &cancelHandle{cancel: cancel}
	// A new stream for the same workflow supersedes any in-flight one.
	if prev, loaded := a.chatCancels.Swap(workflowID, h); loaded {
		if ph, ok := prev.(*cancelHandle); ok {
			ph.cancel()
		}
	}
	go func() {
		defer func() {
			a.chatCancels.CompareAndDelete(workflowID, h)
			cancel()
		}()
		err := a.chatService.StreamChat(
			ctx,
			workflowID, message, providerID, model,
			func(chunk ai.StreamChunk) {
				runtime.EventsEmit(a.ctx, "ai:chunk", map[string]interface{}{
					"workflowID": workflowID,
					"content":    chunk.Content,
					"done":       chunk.Done,
				})
			},
			func(name, args, result string) {
				runtime.EventsEmit(a.ctx, "ai:tool", map[string]interface{}{
					"workflowID": workflowID,
					"tool":       name,
					"args":       args,
					"result":     result,
				})
			},
		)
		if err != nil {
			runtime.EventsEmit(a.ctx, "ai:error", map[string]interface{}{
				"workflowID": workflowID,
				"error":      err.Error(),
			})
		} else {
			// Signal streaming is complete.
			runtime.EventsEmit(a.ctx, "ai:chunk", map[string]interface{}{
				"workflowID": workflowID,
				"content":    "",
				"done":       true,
			})
		}
	}()
	return `{"ok":true}`
}

// StopAIChat cancels an in-flight AI chat stream for the given workflow.
func (a *App) StopAIChat(workflowID string) string {
	if v, ok := a.chatCancels.Load(workflowID); ok {
		if h, ok := v.(*cancelHandle); ok {
			h.cancel()
		}
		return `{"ok":true}`
	}
	return `{"ok":false,"error":"no active stream"}`
}

func (a *App) GetAIChatHistory(workflowID string) string {
	if a.chatService == nil {
		return "[]"
	}
	a.chatService.SetProfileID(a.getActiveProfileID())
	msgs, err := a.chatService.GetHistory(workflowID)
	if err != nil {
		return aiError(err)
	}
	b, _ := json.Marshal(msgs)
	return string(b)
}

func (a *App) ClearAIChatHistory(workflowID string) string {
	if a.chatService == nil {
		return aiError(fmt.Errorf("chat service not initialized"))
	}
	a.chatService.SetProfileID(a.getActiveProfileID())
	if err := a.chatService.ClearHistory(workflowID); err != nil {
		return aiError(err)
	}
	return `{"ok":true}`
}

// GetRunLogs returns the most recent run log entries written by CLI processes.
func (a *App) GetRunLogs(limit int) []LogEntry {
	if a.db == nil || limit <= 0 {
		return nil
	}
	rows, err := a.db.Query(
		`SELECT source, level, message, created_at FROM run_logs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []LogEntry
	for rows.Next() {
		var e LogEntry
		if rows.Scan(&e.Source, &e.Level, &e.Message, &e.Time) == nil {
			out = append(out, e)
		}
	}
	// Reverse so oldest is first.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

