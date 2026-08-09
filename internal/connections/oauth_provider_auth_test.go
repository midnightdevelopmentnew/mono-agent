package connections

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func tokenForm() url.Values {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", "test-code")
	form.Set("redirect_uri", "http://localhost:9876/callback")
	form.Set("client_id", "test-client-id")
	form.Set("client_secret", "test-client-value")
	return form
}

func TestPostFormDefaultProviderUnchanged(t *testing.T) {
	var gotAuth, gotContentType, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"access_token":"tok"}`)
	}))
	defer srv.Close()

	_, status, err := postForm("some-generic-platform", srv.URL, tokenForm())
	if err != nil {
		t.Fatalf("postForm: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if gotAuth != "" {
		t.Errorf("expected no Authorization header for a generic provider, got %q", gotAuth)
	}
	if !strings.Contains(gotContentType, "application/x-www-form-urlencoded") {
		t.Errorf("Content-Type = %q, want form-urlencoded", gotContentType)
	}
	if !strings.Contains(gotBody, "client_id=test-client-id") || !strings.Contains(gotBody, "client_secret=test-client-value") {
		t.Errorf("form body missing client credentials: %q", gotBody)
	}
}

func TestPostFormRedditUsesBasicAuthAndStripsFormCredentials(t *testing.T) {
	var gotUser, gotPass string
	var gotOK bool
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, gotOK = r.BasicAuth()
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"access_token":"tok"}`)
	}))
	defer srv.Close()

	form := tokenForm()
	_, status, err := postForm("reddit", srv.URL, form)
	if err != nil {
		t.Fatalf("postForm: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if !gotOK {
		t.Fatal("expected a Basic Auth header, got none")
	}
	if gotUser != "test-client-id" || gotPass != "test-client-value" {
		t.Errorf("BasicAuth = (%q,%q), want (test-client-id,test-client-value)", gotUser, gotPass)
	}
	if strings.Contains(gotBody, "client_id") || strings.Contains(gotBody, "client_secret") {
		t.Errorf("form body should not carry credentials once they're in the Basic Auth header: %q", gotBody)
	}
	if !strings.Contains(gotBody, "code=test-code") {
		t.Errorf("form body missing code: %q", gotBody)
	}

	// The caller's form map must survive untouched — PostTokenRequestWithAudienceFallback
	// reuses it for the Microsoft-audience-fallback retry.
	if form.Get("client_id") != "test-client-id" || form.Get("client_secret") != "test-client-value" {
		t.Error("postForm mutated the caller's form map")
	}
}

func TestPostFormNotionSendsJSONBodyAndBasicAuth(t *testing.T) {
	var gotUser, gotPass string
	var gotOK bool
	var gotContentType string
	var gotPayload map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, gotOK = r.BasicAuth()
		gotContentType = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotPayload)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"access_token":"tok"}`)
	}))
	defer srv.Close()

	_, status, err := postForm("notion", srv.URL, tokenForm())
	if err != nil {
		t.Fatalf("postForm: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if !gotOK || gotUser != "test-client-id" || gotPass != "test-client-value" {
		t.Errorf("BasicAuth = (%q,%q,%v), want (test-client-id,test-client-value,true)", gotUser, gotPass, gotOK)
	}
	if !strings.Contains(gotContentType, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if gotPayload["grant_type"] != "authorization_code" || gotPayload["code"] != "test-code" || gotPayload["redirect_uri"] != "http://localhost:9876/callback" {
		t.Errorf("unexpected JSON payload: %+v", gotPayload)
	}
	if _, ok := gotPayload["client_id"]; ok {
		t.Error("JSON payload must not carry client_id — Notion rejects it as a body field")
	}
}
