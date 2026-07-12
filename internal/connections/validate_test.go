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
