package comm

import (
	"strings"
	"testing"
)

// TestBuildMIMEMessageStripsHeaderInjection is a regression test: a subject
// (or address) templated from untrusted upstream data must not be able to
// inject extra SMTP headers or a second body via embedded CRLF sequences.
func TestBuildMIMEMessageStripsHeaderInjection(t *testing.T) {
	msg, err := buildMIMEMessage(
		"sender@example.com",
		[]string{"rcpt@example.com"},
		nil,
		"Re: hi\r\nBcc: attacker@evil.com\r\n\r\ninjected body",
		"legit body",
		"text",
		nil,
	)
	if err != nil {
		t.Fatalf("buildMIMEMessage: %v", err)
	}

	for _, line := range strings.Split(string(msg), "\r\n") {
		if strings.HasPrefix(line, "Bcc:") {
			t.Errorf("injected Bcc header line present: %q\nfull message:\n%s", line, msg)
		}
	}
}
