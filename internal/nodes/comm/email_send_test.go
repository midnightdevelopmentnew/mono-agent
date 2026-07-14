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
		"",
		"",
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

// TestToStringSliceSplitsCommaSeparatedAddresses is a regression test: a
// single "a@x.com,b@x.com" config value must become two envelope recipients,
// not one malformed address passed straight to smtp.SendMail.
func TestToStringSliceSplitsCommaSeparatedAddresses(t *testing.T) {
	got := toStringSlice("a@x.com, b@x.com;c@x.com")
	want := []string{"a@x.com", "b@x.com", "c@x.com"}
	if len(got) != len(want) {
		t.Fatalf("toStringSlice: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("toStringSlice: got %v, want %v", got, want)
		}
	}
}

// TestBuildMIMEMessageThreading verifies in_reply_to/references become
// In-Reply-To/References headers, threading the message on the same
// conversation instead of starting a new one.
func TestBuildMIMEMessageThreading(t *testing.T) {
	msg, err := buildMIMEMessage(
		"sender@example.com",
		[]string{"rcpt@example.com"},
		nil,
		"Re: hi",
		"body",
		"text",
		nil,
		"<orig@example.com>",
		"<orig@example.com>",
	)
	if err != nil {
		t.Fatalf("buildMIMEMessage: %v", err)
	}
	s := string(msg)
	if !strings.Contains(s, "In-Reply-To: <orig@example.com>\r\n") {
		t.Errorf("missing In-Reply-To header:\n%s", s)
	}
	if !strings.Contains(s, "References: <orig@example.com>\r\n") {
		t.Errorf("missing References header:\n%s", s)
	}
}
