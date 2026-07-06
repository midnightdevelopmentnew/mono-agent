package workflow

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

// TestWebhookAuthHeaderEnforced is a regression test: the "auth_header"/"auth_token"
// webhook config fields (exposed by both the GUI and the JSON schema) were
// previously read nowhere in ServeHTTP, so a webhook configured with an auth
// token was silently unauthenticated — anyone who found the path could trigger it.
func TestWebhookAuthHeaderEnforced(t *testing.T) {
	s := NewWebhookServer(":0", zerolog.Nop())
	fired := false
	if err := s.Register(&WebhookRegistration{
		Path:       "secure-hook",
		Method:     "POST",
		AuthHeader: "X-Webhook-Secret",
		AuthToken:  "s3cr3t",
		TriggerFn:  func(items []Item) { fired = true },
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// No auth header at all → rejected.
	req := httptest.NewRequest(http.MethodPost, "/webhook/secure-hook", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing header: status = %d, want 401", rec.Code)
	}
	if fired {
		t.Fatal("missing header: trigger fired, want rejected")
	}

	// Wrong token → rejected.
	req = httptest.NewRequest(http.MethodPost, "/webhook/secure-hook", nil)
	req.Header.Set("X-Webhook-Secret", "wrong")
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: status = %d, want 401", rec.Code)
	}
	if fired {
		t.Fatal("wrong token: trigger fired, want rejected")
	}

	// Correct token → accepted.
	req = httptest.NewRequest(http.MethodPost, "/webhook/secure-hook", nil)
	req.Header.Set("X-Webhook-Secret", "s3cr3t")
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("correct token: status = %d, want 200", rec.Code)
	}
	if !fired {
		t.Fatal("correct token: trigger did not fire")
	}
}

// TestWebhookCORSHeadersForAuthHeader is a regression test: the CORS-header
// gate only checked reg.HMACSecret, so a webhook secured via AuthHeader (with
// no HMAC secret) never got CORS headers, breaking legitimate browser-based
// callers using that auth mechanism.
func TestWebhookCORSHeadersForAuthHeader(t *testing.T) {
	s := NewWebhookServer(":0", zerolog.Nop())
	if err := s.Register(&WebhookRegistration{
		Path:       "cors-hook",
		Method:     "POST",
		AuthHeader: "X-Webhook-Secret",
		AuthToken:  "s3cr3t",
		TriggerFn:  func(items []Item) {},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/webhook/cors-hook", nil)
	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("X-Webhook-Secret", "s3cr3t")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://example.com" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want echoed origin", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(got, "X-Webhook-Secret") {
		t.Fatalf("Access-Control-Allow-Headers = %q, want it to include X-Webhook-Secret", got)
	}
}
