package secrets

import (
	"context"
	"testing"
)

func TestResolve_SuccessfulResolution(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()

	if _, err := Add(ctx, db.DB, "default", "secret", "e1", map[string]string{"secret": "v-alpha1"}, "", "", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}

	got, err := Resolve(ctx, db.DB, "default", "@secret:e1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "v-alpha1" {
		t.Fatalf("got %q, want %q", got, "v-alpha1")
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

	_, err := Resolve(ctx, db.DB, "default", "@secret:e0")
	if err == nil {
		t.Fatal("expected error for missing secret, got nil")
	}
}

// Covers an entry with exactly one field whose key isn't literally
// "secret" — Resolve should still return it, since there's no ambiguity
// about which field a bare @secret:name reference means.
func TestResolve_UsesSoleFieldWhenNotNamedSecret(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	if _, err := Add(ctx, db.DB, "default", "secret", "e2", map[string]string{"token": "tok-one1"}, "", "", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}

	got, err := Resolve(ctx, db.DB, "default", "@secret:e2")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "tok-one1" {
		t.Fatalf("got %q, want %q", got, "tok-one1")
	}
}

// Covers an entry with several fields, one of them keyed "secret" — that
// one wins for a bare reference, since it's unambiguous which value is
// meant.
func TestResolve_PrefersSecretKeyAmongMultipleFields(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	if _, err := Add(ctx, db.DB, "default", "secret", "e3", map[string]string{
		"secret":  "v-pref1",
		"field_a": "fa-one1",
	}, "", "", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}

	got, err := Resolve(ctx, db.DB, "default", "@secret:e3")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "v-pref1" {
		t.Fatalf("got %q, want %q", got, "v-pref1")
	}
}

// Covers an entry with several fields, none keyed "secret" — Resolve can't
// disambiguate and must error rather than silently pick one.
func TestResolve_ErrorsOnMultipleFieldsWithoutSecretKey(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	if _, err := Add(ctx, db.DB, "default", "secret", "e4", map[string]string{
		"field_a": "fa-one1",
		"field_b": "fb-one1",
	}, "", "", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if _, err := Resolve(ctx, db.DB, "default", "@secret:e4"); err == nil {
		t.Fatal("expected an error picking between two equally-plausible fields, got nil")
	}
}

func TestResolveConfig_SuccessfulResolution(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	if _, err := Add(ctx, db.DB, "default", "secret", "e1", map[string]string{"secret": "v-alpha1"}, "", "", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}

	config := map[string]interface{}{
		"api_key": "@secret:e1",
	}
	if err := ResolveConfig(ctx, db.DB, "default", config); err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	if config["api_key"] != "v-alpha1" {
		t.Fatalf("got %v, want %q", config["api_key"], "v-alpha1")
	}
}

// Verifies ResolveConfig's deliberate fail-open behavior (matching
// vault.ResolveConfig's @img- convention): a missing entry must not error
// out the whole config resolution, and must leave the original ref in
// place.
func TestResolveConfig_MissingSecretLeavesRefUnchanged(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()

	config := map[string]interface{}{
		"api_key": "@secret:e0",
	}
	if err := ResolveConfig(ctx, db.DB, "default", config); err != nil {
		t.Fatalf("ResolveConfig must not error on a missing entry: %v", err)
	}
	if config["api_key"] != "@secret:e0" {
		t.Fatalf("expected ref left unchanged, got %v", config["api_key"])
	}
}

// Verifies a config with mixed matching/non-matching values only has the
// matching ones replaced.
func TestResolveConfig_OnlyReplacesMatchingValues(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	if _, err := Add(ctx, db.DB, "default", "secret", "e1", map[string]string{"secret": "v-alpha1"}, "", "", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}

	config := map[string]interface{}{
		"api_key":   "@secret:e1",
		"model":     "gpt-4",
		"max_calls": 5,
		"nested":    map[string]interface{}{"still": "untouched"},
	}
	if err := ResolveConfig(ctx, db.DB, "default", config); err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	if config["api_key"] != "v-alpha1" {
		t.Fatalf("api_key: got %v, want %q", config["api_key"], "v-alpha1")
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
