package connections

import (
	"context"
	"strings"
	"testing"
)

// TestValidateConnectionMasksDBPassword is a regression test: for database
// platforms ValidateConnection previously returned the first 30 chars of the
// raw connection_string as the AccountID, leaking the embedded password into
// AccountID/Label — which survive Redact() and appear in `connect list --json`
// and GUI output.
func TestValidateConnectionMasksDBPassword(t *testing.T) {
	for _, platform := range []string{"postgresql", "mysql", "mongodb", "redis"} {
		conn := &Connection{
			Platform: platform,
			Data: map[string]interface{}{
				"connection_string": "postgres://admin:S3cretPass@db.example.com:5432/prod",
			},
		}
		accountID, err := ValidateConnection(context.Background(), conn)
		if err != nil {
			t.Fatalf("%s: ValidateConnection: %v", platform, err)
		}
		if strings.Contains(accountID, "S3cretPass") {
			t.Fatalf("%s: accountID leaked password: %q", platform, accountID)
		}
		if accountID == "" {
			t.Fatalf("%s: accountID empty, expected a masked identifier", platform)
		}
	}
}

// TestValidateConnectionUnparseableConnStr verifies that a key=value DSN (which
// url.Parse cannot decompose) yields a generic label rather than the raw
// string, so a password embedded in it is never surfaced.
func TestValidateConnectionUnparseableConnStr(t *testing.T) {
	conn := &Connection{
		Platform: "postgresql",
		Data: map[string]interface{}{
			"connection_string": "host=localhost user=admin password=S3cretPass dbname=prod",
		},
	}
	accountID, err := ValidateConnection(context.Background(), conn)
	if err != nil {
		t.Fatalf("ValidateConnection: %v", err)
	}
	if strings.Contains(accountID, "S3cretPass") {
		t.Fatalf("accountID leaked password: %q", accountID)
	}
}

// TestValidateSSHMissingFields verifies validateSSH rejects a connection
// missing host/username before attempting any network dial.
func TestValidateSSHMissingFields(t *testing.T) {
	for _, platform := range []string{"ssh", "sftp"} {
		conn := &Connection{Platform: platform, Data: map[string]interface{}{"username": "root"}}
		if _, err := ValidateConnection(context.Background(), conn); err == nil {
			t.Fatalf("%s: expected error for missing host", platform)
		}
	}
}

// TestValidateSSHMissingAuth verifies validateSSH rejects a connection with
// host/username but neither a password nor a private_key.
func TestValidateSSHMissingAuth(t *testing.T) {
	conn := &Connection{
		Platform: "ssh",
		Data:     map[string]interface{}{"host": "example.com", "username": "root"},
	}
	if _, err := ValidateConnection(context.Background(), conn); err == nil {
		t.Fatal("expected error for missing password/private_key")
	}
}

// TestValidateFTPMissingFields verifies validateFTP rejects a connection
// missing host/username before attempting any network dial.
func TestValidateFTPMissingFields(t *testing.T) {
	conn := &Connection{Platform: "ftp", Data: map[string]interface{}{"username": "anon"}}
	if _, err := ValidateConnection(context.Background(), conn); err == nil {
		t.Fatal("expected error for missing host")
	}
}

// TestValidateGenericMissingBaseURL verifies the generic_api/generic_basic
// cases reject a connection missing base_url.
func TestValidateGenericMissingBaseURL(t *testing.T) {
	for _, platform := range []string{"generic_api", "generic_basic"} {
		conn := &Connection{Platform: platform, Data: map[string]interface{}{}}
		if _, err := ValidateConnection(context.Background(), conn); err == nil {
			t.Fatalf("%s: expected error for missing base_url", platform)
		}
	}
}

// TestLinearAuthHeaderPrefersAPIKeyAsIs verifies a personal API key is sent
// unprefixed — Linear rejects a "Bearer " prefix on API keys.
func TestLinearAuthHeaderPrefersAPIKeyAsIs(t *testing.T) {
	got := linearAuthHeader(map[string]interface{}{"api_key": "lin_api_example"})
	if got != "lin_api_example" {
		t.Errorf("linearAuthHeader = %q, want unprefixed api_key value", got)
	}
}

// TestLinearAuthHeaderPrefixesOAuthAccessToken is a regression test: OAuth
// access tokens were previously sent without the required "Bearer " prefix,
// which authenticated with neither auth style and blocked Linear OAuth
// connections entirely.
func TestLinearAuthHeaderPrefixesOAuthAccessToken(t *testing.T) {
	got := linearAuthHeader(map[string]interface{}{"access_token": "oauth-example-token"})
	want := "Bearer oauth-example-token"
	if got != want {
		t.Errorf("linearAuthHeader = %q, want %q", got, want)
	}
}

// TestLinearAuthHeaderAPIKeyTakesPriority verifies api_key wins when both
// fields happen to be present, matching the pre-existing lookup order.
func TestLinearAuthHeaderAPIKeyTakesPriority(t *testing.T) {
	got := linearAuthHeader(map[string]interface{}{
		"api_key":      "lin_api_example",
		"access_token": "oauth-example-token",
	})
	if got != "lin_api_example" {
		t.Errorf("linearAuthHeader = %q, want api_key to take priority", got)
	}
}

// TestLinearAuthHeaderEmptyWhenNoCredentials verifies the empty-credentials
// case returns "" rather than a malformed header like "Bearer ".
func TestLinearAuthHeaderEmptyWhenNoCredentials(t *testing.T) {
	if got := linearAuthHeader(map[string]interface{}{}); got != "" {
		t.Errorf("linearAuthHeader = %q, want empty string", got)
	}
}
