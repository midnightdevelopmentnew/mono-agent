package ai

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"monoagent/internal/secrets"
)

// vaultCredentialFieldName is the vault field key SaveProvider/GetProvider
// use to store/resolve a provider's credential — the AI-provider analogue
// of the "cookies" field key crawler sessions use and the
// "access_token"/etc. keys connections use.
const vaultCredentialFieldName = "api_key"

// AIProvider represents a user-configured AI provider instance.
type AIProvider struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ProviderID   string `json:"provider_id"`   // references registry e.g. "openai"
	Tier         string `json:"tier"`           // "known" | "gateway"
	APIKey       string `json:"api_key"`
	BaseURL      string `json:"base_url"`
	DefaultModel string `json:"default_model"`
	ExtraHeaders string `json:"extra_headers"` // JSON string
	Status       string `json:"status"`        // "active" | "error" | "untested"
	LastTested   string `json:"last_tested"`
	ProfileID    string `json:"profile_id,omitempty"`
	VaultRef     string `json:"vault_ref,omitempty"`
	CreatedAt    string `json:"created_at"`
}

// MarshalJSON omits the plaintext APIKey from any serialized projection so the
// stored provider secret never crosses an output boundary (GUI IPC responses,
// CLI JSON). The key is still read from and written to SQLite via the struct
// field directly, so provider clients continue to authenticate normally.
func (p AIProvider) MarshalJSON() ([]byte, error) {
	type alias AIProvider
	safe := alias(p)
	safe.APIKey = ""
	return json.Marshal(safe)
}

// ChatMessage represents a single message in an AI chat conversation.
type ChatMessage struct {
	ID         string `json:"id"`
	WorkflowID string `json:"workflow_id"`
	Role       string `json:"role"`                    // "user" | "assistant" | "tool"
	Content    string `json:"content"`
	ToolCalls  string `json:"tool_calls,omitempty"`     // JSON array
	ToolCallID string `json:"tool_call_id,omitempty"`   // For tool result messages
	ProviderID string `json:"provider_id,omitempty"`
	Model      string `json:"model,omitempty"`
	TokenCount int    `json:"token_count,omitempty"`
	CreatedAt  string `json:"created_at"`
}

// AIStore provides persistence for AI providers and chat messages.
type AIStore struct {
	db *sql.DB
}

// NewAIStore creates a new AIStore and ensures the required tables exist.
func NewAIStore(db *sql.DB) (*AIStore, error) {
	s := &AIStore{db: db}
	if err := s.initTables(); err != nil {
		return nil, fmt.Errorf("ai store: init tables: %w", err)
	}
	return s, nil
}

func (s *AIStore) initTables() error {
	const providersSQL = `CREATE TABLE IF NOT EXISTS ai_providers (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		provider_id TEXT NOT NULL,
		tier TEXT NOT NULL,
		api_key TEXT NOT NULL,
		base_url TEXT NOT NULL DEFAULT '',
		default_model TEXT NOT NULL DEFAULT '',
		extra_headers TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'untested',
		last_tested TEXT NOT NULL DEFAULT '',
		profile_id TEXT NOT NULL DEFAULT 'default',
		created_at TEXT NOT NULL
	)`

	const messagesSQL = `CREATE TABLE IF NOT EXISTS ai_chat_messages (
		id TEXT PRIMARY KEY,
		workflow_id TEXT NOT NULL,
		role TEXT NOT NULL,
		content TEXT NOT NULL,
		tool_calls TEXT NOT NULL DEFAULT '',
		tool_call_id TEXT NOT NULL DEFAULT '',
		provider_id TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		token_count INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL
	)`

	if _, err := s.db.Exec(providersSQL); err != nil {
		return fmt.Errorf("create ai_providers: %w", err)
	}
	if _, err := s.db.Exec(messagesSQL); err != nil {
		return fmt.Errorf("create ai_chat_messages: %w", err)
	}
	// Migrate: add columns that may be missing on existing DBs. SQLite errors
	// if the column already exists; that error is expected and ignored.
	s.db.Exec(`ALTER TABLE ai_chat_messages ADD COLUMN tool_call_id TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE ai_providers ADD COLUMN profile_id TEXT NOT NULL DEFAULT 'default'`)
	s.db.Exec(`ALTER TABLE ai_providers ADD COLUMN vault_ref TEXT NOT NULL DEFAULT ''`)
	return nil
}

// SaveProvider upserts an AI provider. If CreatedAt is empty it is set to now.
// ProfileID defaults to "default" if unset; it is intentionally excluded from
// the ON CONFLICT update clause so an existing provider cannot be re-assigned
// to a different profile via an upsert.
func (s *AIStore) SaveProvider(p AIProvider) error {
	if p.ProfileID == "" {
		p.ProfileID = "default"
	}

	providedCredential := p.APIKey
	if providedCredential != "" {
		vaultFields := make(map[string]string)
		vaultFields[vaultCredentialFieldName] = providedCredential
		vaultRef, err := secrets.PutSystemEntry(context.Background(), s.db, p.ProfileID, "ai_provider", p.VaultRef, p.Name, vaultFields, "", "")
		if err != nil {
			return fmt.Errorf("saving provider credential to vault: %w", err)
		}
		p.VaultRef = vaultRef
	}

	if p.CreatedAt == "" {
		p.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	const q = `INSERT INTO ai_providers (id, name, provider_id, tier, api_key, base_url, default_model, extra_headers, status, last_tested, profile_id, vault_ref, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name,
			provider_id=excluded.provider_id,
			tier=excluded.tier,
			api_key=excluded.api_key,
			base_url=excluded.base_url,
			default_model=excluded.default_model,
			extra_headers=excluded.extra_headers,
			status=excluded.status,
			last_tested=excluded.last_tested,
			vault_ref=excluded.vault_ref,
			created_at=excluded.created_at`
	_, err := s.db.Exec(q,
		p.ID, p.Name, p.ProviderID, p.Tier, "",
		p.BaseURL, p.DefaultModel, p.ExtraHeaders,
		p.Status, p.LastTested, p.ProfileID, p.VaultRef, p.CreatedAt,
	)
	return err
}

// GetProvider retrieves a single provider by ID, scoped to profileID.
func (s *AIStore) GetProvider(id, profileID string) (AIProvider, error) {
	if profileID == "" {
		profileID = "default"
	}
	const q = `SELECT id, name, provider_id, tier, api_key, base_url, default_model, extra_headers, status, last_tested, profile_id, COALESCE(vault_ref,''), created_at
		FROM ai_providers WHERE id = ? AND COALESCE(profile_id,'default') = ?`
	var p AIProvider
	err := s.db.QueryRow(q, id, profileID).Scan(
		&p.ID, &p.Name, &p.ProviderID, &p.Tier, &p.APIKey,
		&p.BaseURL, &p.DefaultModel, &p.ExtraHeaders,
		&p.Status, &p.LastTested, &p.ProfileID, &p.VaultRef, &p.CreatedAt,
	)
	if err != nil {
		return AIProvider{}, err
	}
	if p.VaultRef == "" {
		return p, nil
	}
	vaultFields, _, resolveErr := secrets.DecryptFields(context.Background(), s.db, profileID, p.VaultRef)
	if resolveErr != nil {
		return AIProvider{}, fmt.Errorf("resolving provider credential from vault: %w", resolveErr)
	}
	cred := vaultFields[vaultCredentialFieldName]
	p.APIKey = cred
	return p, nil
}

// ListProviders returns all providers for profileID, ordered by created_at descending.
func (s *AIStore) ListProviders(profileID string) ([]AIProvider, error) {
	if profileID == "" {
		profileID = "default"
	}
	const q = `SELECT id, name, provider_id, tier, base_url, default_model, extra_headers, status, last_tested, profile_id, COALESCE(vault_ref,''), created_at
		FROM ai_providers WHERE COALESCE(profile_id,'default') = ? ORDER BY created_at DESC`
	rows, err := s.db.Query(q, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var providers []AIProvider
	for rows.Next() {
		var p AIProvider
		if err := rows.Scan(
			&p.ID, &p.Name, &p.ProviderID, &p.Tier,
			&p.BaseURL, &p.DefaultModel, &p.ExtraHeaders,
			&p.Status, &p.LastTested, &p.ProfileID, &p.VaultRef, &p.CreatedAt,
		); err != nil {
			return nil, err
		}
		providers = append(providers, p)
	}
	return providers, rows.Err()
}

// DeleteProvider removes a provider by ID, scoped to profileID.
func (s *AIStore) DeleteProvider(id, profileID string) error {
	if profileID == "" {
		profileID = "default"
	}
	_, err := s.db.Exec(`DELETE FROM ai_providers WHERE id = ? AND COALESCE(profile_id,'default') = ?`, id, profileID)
	return err
}

// UpdateProviderStatus updates the status and last_tested fields for a provider, scoped to profileID.
func (s *AIStore) UpdateProviderStatus(id, status, lastTested, profileID string) error {
	if profileID == "" {
		profileID = "default"
	}
	_, err := s.db.Exec(
		`UPDATE ai_providers SET status = ?, last_tested = ? WHERE id = ? AND COALESCE(profile_id,'default') = ?`,
		status, lastTested, id, profileID,
	)
	return err
}

// SaveChatMessage inserts a chat message. If CreatedAt is empty it is set to now.
func (s *AIStore) SaveChatMessage(m ChatMessage) error {
	if m.CreatedAt == "" {
		m.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	const q = `INSERT INTO ai_chat_messages (id, workflow_id, role, content, tool_calls, tool_call_id, provider_id, model, token_count, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := s.db.Exec(q,
		m.ID, m.WorkflowID, m.Role, m.Content,
		m.ToolCalls, m.ToolCallID, m.ProviderID, m.Model,
		m.TokenCount, m.CreatedAt,
	)
	return err
}

// GetChatHistory returns all messages for a workflow ordered by created_at ascending.
func (s *AIStore) GetChatHistory(workflowID string) ([]ChatMessage, error) {
	const q = `SELECT id, workflow_id, role, content, tool_calls, tool_call_id, provider_id, model, token_count, created_at
		FROM ai_chat_messages WHERE workflow_id = ? ORDER BY created_at ASC`
	rows, err := s.db.Query(q, workflowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []ChatMessage
	for rows.Next() {
		var m ChatMessage
		if err := rows.Scan(
			&m.ID, &m.WorkflowID, &m.Role, &m.Content,
			&m.ToolCalls, &m.ToolCallID, &m.ProviderID, &m.Model,
			&m.TokenCount, &m.CreatedAt,
		); err != nil {
			return nil, err
		}
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

// ClearChatHistory deletes all messages for a given workflow.
func (s *AIStore) ClearChatHistory(workflowID string) error {
	_, err := s.db.Exec(`DELETE FROM ai_chat_messages WHERE workflow_id = ?`, workflowID)
	return err
}
