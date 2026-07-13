package secrets

import (
	"context"
	"testing"
)

func TestResolve_SuccessfulResolution(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()

	if _, err := Add(ctx, db.DB, "default", "secret", "openai-key", "sk-abc123", "", "", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}

	got, err := Resolve(ctx, db.DB, "default", "@secret:openai-key")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "sk-abc123" {
		t.Fatalf("got %q, want %q", got, "sk-abc123")
	}
}

func TestResolve_NonSecretRefReturnedUnchanged(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()

	got, err := Resolve(ctx, db.DB, "default", "plain-value")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "plain-value" {
		t.Fatalf("got %q, want %q", got, "plain-value")
	}
}

func TestResolve_MissingSecretErrors(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()

	_, err := Resolve(ctx, db.DB, "default", "@secret:does-not-exist")
	if err == nil {
		t.Fatal("expected error for missing secret, got nil")
	}
}

func TestResolveConfig_SuccessfulResolution(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	if _, err := Add(ctx, db.DB, "default", "secret", "openai-key", "sk-abc123", "", "", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}

	config := map[string]interface{}{
		"api_key": "@secret:openai-key",
	}
	if err := ResolveConfig(ctx, db.DB, "default", config); err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	if config["api_key"] != "sk-abc123" {
		t.Fatalf("got %v, want %q", config["api_key"], "sk-abc123")
	}
}

// TestResolveConfig_MissingSecretLeavesRefUnchanged verifies ResolveConfig's
// deliberate fail-open behavior (matching vault.ResolveConfig's @img-
// convention): a missing secret must not error out the whole config
// resolution, and must leave the original "@secret:" ref in place.
func TestResolveConfig_MissingSecretLeavesRefUnchanged(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()

	config := map[string]interface{}{
		"api_key": "@secret:does-not-exist",
	}
	if err := ResolveConfig(ctx, db.DB, "default", config); err != nil {
		t.Fatalf("ResolveConfig must not error on a missing secret: %v", err)
	}
	if config["api_key"] != "@secret:does-not-exist" {
		t.Fatalf("expected ref left unchanged, got %v", config["api_key"])
	}
}

// TestResolveConfig_OnlyReplacesMatchingValues verifies a config with mixed
// @secret:/non-@secret: values only has the matching ones replaced.
func TestResolveConfig_OnlyReplacesMatchingValues(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	if _, err := Add(ctx, db.DB, "default", "secret", "openai-key", "sk-abc123", "", "", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}

	config := map[string]interface{}{
		"api_key":   "@secret:openai-key",
		"model":     "gpt-4",
		"max_calls": 5,
		"nested":    map[string]interface{}{"still": "untouched"},
	}
	if err := ResolveConfig(ctx, db.DB, "default", config); err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	if config["api_key"] != "sk-abc123" {
		t.Fatalf("api_key: got %v, want %q", config["api_key"], "sk-abc123")
	}
	if config["model"] != "gpt-4" {
		t.Fatalf("model should be untouched, got %v", config["model"])
	}
	if config["max_calls"] != 5 {
		t.Fatalf("max_calls should be untouched, got %v", config["max_calls"])
	}
	nested, ok := config["nested"].(map[string]interface{})
	if !ok || nested["still"] != "untouched" {
		t.Fatalf("nested should be untouched, got %v", config["nested"])
	}
}
