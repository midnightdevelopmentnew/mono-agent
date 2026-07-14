package service

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestGmailBuildRFC2822IncludesCcBccAndThreadingHeaders(t *testing.T) {
	raw, err := gmailBuildRFC2822(gmailMessageOpts{
		From:       "me@example.com",
		To:         "a@x.com,b@x.com",
		Cc:         "c@x.com",
		Bcc:        "d@x.com",
		Subject:    "Re: hi",
		Body:       "body",
		BodyType:   "text",
		InReplyTo:  "<orig@x.com>",
		References: "<orig@x.com>",
	})
	if err != nil {
		t.Fatalf("gmailBuildRFC2822: %v", err)
	}
	decoded := gmailDecodeForTest(t, raw)
	for _, want := range []string{
		"To: a@x.com,b@x.com\r\n",
		"Cc: c@x.com\r\n",
		"Bcc: d@x.com\r\n",
		"In-Reply-To: <orig@x.com>\r\n",
		"References: <orig@x.com>\r\n",
	} {
		if !strings.Contains(decoded, want) {
			t.Errorf("missing %q in message:\n%s", want, decoded)
		}
	}
}

func TestGmailBuildRFC2822StripsHeaderInjection(t *testing.T) {
	raw, err := gmailBuildRFC2822(gmailMessageOpts{
		From:    "me@example.com",
		To:      "a@x.com",
		Subject: "hi\r\nBcc: attacker@evil.com",
		Body:    "body",
	})
	if err != nil {
		t.Fatalf("gmailBuildRFC2822: %v", err)
	}
	decoded := gmailDecodeForTest(t, raw)
	for _, line := range strings.Split(decoded, "\r\n") {
		if strings.HasPrefix(line, "Bcc:") {
			t.Errorf("injected Bcc header line present: %q\nfull message:\n%s", line, decoded)
		}
	}
}

func TestGmailSplitAddressList(t *testing.T) {
	got := gmailSplitAddressList("A Name <a@x.com>, b@x.com")
	want := []string{"A Name <a@x.com>", "b@x.com"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("gmailSplitAddressList: got %v, want %v", got, want)
	}
}

func TestGmailBareAddress(t *testing.T) {
	cases := map[string]string{
		"A Name <a@x.com>": "a@x.com",
		"a@x.com":          "a@x.com",
		"  b@x.com  ":      "b@x.com",
	}
	for in, want := range cases {
		if got := gmailBareAddress(in); got != want {
			t.Errorf("gmailBareAddress(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPrefixReplySubjectGmail(t *testing.T) {
	if got := prefixReplySubjectGmail("hi"); got != "Re: hi" {
		t.Errorf("prefixReplySubjectGmail(%q) = %q", "hi", got)
	}
	if got := prefixReplySubjectGmail("Re: hi"); got != "Re: hi" {
		t.Errorf("prefixReplySubjectGmail should not double-prefix, got %q", got)
	}
}

func gmailDecodeForTest(t *testing.T, raw string) string {
	t.Helper()
	b, err := base64.URLEncoding.DecodeString(raw)
	if err != nil {
		t.Fatalf("decoding raw message: %v", err)
	}
	return string(b)
}
