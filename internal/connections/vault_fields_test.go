package connections

import (
	"reflect"
	"testing"
)

func TestSplitSecretFields_OAuthMovesOnlyTokens(t *testing.T) {
	accessToken := "PLACEHOLDER-one"
	refreshToken := "PLACEHOLDER-ref-one"
	data := map[string]interface{}{
		"access_token":  accessToken,
		"refresh_token": refreshToken,
		"token_type":    "Bearer",
		"scope":         "repo",
		"expires_at":    "2026-01-01T00:00:00Z",
	}
	secretFields, nonSecret := splitSecretFields("github", MethodOAuth, data)

	wantSecret := map[string]string{"access_token": accessToken, "refresh_token": refreshToken}
	if !reflect.DeepEqual(secretFields, wantSecret) {
		t.Fatalf("secretFields = %+v, want %+v", secretFields, wantSecret)
	}
	wantNonSecret := map[string]interface{}{"token_type": "Bearer", "scope": "repo", "expires_at": "2026-01-01T00:00:00Z"}
	if !reflect.DeepEqual(nonSecret, wantNonSecret) {
		t.Fatalf("nonSecret = %+v, want %+v", nonSecret, wantNonSecret)
	}
}

func TestSplitSecretFields_APIKeyMethodUsesRegistrySecretFlag(t *testing.T) {
	accessToken := "PLACEHOLDER-one"
	data := map[string]interface{}{
		"instance_url": "https://fosstodon.org",
		"access_token": accessToken,
	}
	secretFields, nonSecret := splitSecretFields("mastodon", MethodAPIKey, data)

	if secretFields["access_token"] != accessToken {
		t.Fatalf("expected access_token to be a secret field, got %+v", secretFields)
	}
	if _, isSecret := secretFields["instance_url"]; isSecret {
		t.Fatal("instance_url must not be treated as secret")
	}
	if nonSecret["instance_url"] != "https://fosstodon.org" {
		t.Fatalf("expected instance_url to stay in nonSecret, got %+v", nonSecret)
	}
}

func TestSplitSecretFields_BrowserMethodHasNoSecretFields(t *testing.T) {
	data := map[string]interface{}{}
	secretFields, _ := splitSecretFields("instagram", MethodBrowser, data)
	if len(secretFields) != 0 {
		t.Fatalf("expected no secret fields for a browser-session platform, got %+v", secretFields)
	}
}

func TestConnectionVaultName(t *testing.T) {
	c := &Connection{Platform: "github", Label: "Personal"}
	if got := connectionVaultName(c); got != "GitHub API — Personal" {
		t.Fatalf("got %q, want %q", got, "GitHub API — Personal")
	}

	c2 := &Connection{Platform: "github", AccountID: "octocat"}
	if got := connectionVaultName(c2); got != "GitHub API — octocat" {
		t.Fatalf("got %q, want %q", got, "GitHub API — octocat")
	}

	c3 := &Connection{Platform: "unknown_platform"}
	if got := connectionVaultName(c3); got != "unknown_platform" {
		t.Fatalf("got %q, want %q", got, "unknown_platform")
	}
}
